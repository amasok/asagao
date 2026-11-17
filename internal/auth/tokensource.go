package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth/tokenstore"
	"github.com/amasok/asagao/internal/config"
)

// ErrNotLoggedIn is returned when neither ASANA_TOKEN nor stored credentials exist.
var ErrNotLoggedIn = errors.New("ログインしていません。`asagao auth login --pat` で Personal Access Token（" + PATConsoleURL +
	" で作成）を登録するか、`asagao auth login` で OAuth ログインしてください")

// ErrLoginReplaced is returned by a running OAuth session when the store now holds a login
// for another (or an unknown) user, made meanwhile in another process. It is not adopted:
// the caller must build its client again, so the account check of the next sync applies
// (sync.ErrAccountChanged) and nothing is sent as the other user by this session.
var ErrLoginReplaced = errors.New("別の場所で別のアカウントにログインし直されました。新しいログインで同期し直します")

// EnvToken is the environment variable for a Personal Access Token (fallback for dev / headless).
const EnvToken = "ASANA_TOKEN"

// PATConsoleURL is where a user creates a Personal Access Token.
const PATConsoleURL = "https://app.asana.com/0/my-apps"

// Source is the token source for API calls, with how the user is authenticated.
type Source struct {
	oauth2.TokenSource
	Method tokenstore.Method
	// FromEnv is true when the token comes from ASANA_TOKEN rather than the token store.
	FromEnv bool
}

// UnauthorizedHint tells the user what to do when Asana rejects the token (401).
func (s *Source) UnauthorizedHint() string {
	switch {
	case s.FromEnv:
		return EnvToken + " の Personal Access Token が無効です。新しいトークンを " + PATConsoleURL + " で作り、" + EnvToken + " に設定し直してください"
	case s.Method == tokenstore.MethodPAT:
		return "保存した Personal Access Token が無効です。`asagao auth login --pat` で新しいトークン（" + PATConsoleURL + " で作成）を登録してください"
	}
	return "ログインが無効になっています。`asagao auth login` でログインし直してください"
}

// LoginHelp returns what the user should do when err means they must log in (again):
// nothing stored, or a token Asana rejects. It returns "" for any other error.
func LoginHelp(err error) string {
	var apiErr *asana.Error
	switch {
	case errors.Is(err, ErrNotLoggedIn):
		return ErrNotLoggedIn.Error()
	case errors.Is(err, ErrLoginReplaced):
		return ErrLoginReplaced.Error()
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		if apiErr.Hint != "" {
			return apiErr.Hint
		}
		return "Asana がトークンを受け付けません。`asagao auth login --pat` か `asagao auth login` でログインし直してください"
	}
	return ""
}

// TokenSource returns a token source for API calls.
// ASANA_TOKEN (a Personal Access Token) takes precedence over stored credentials. A stored
// PAT is used as is (it has no refresh token); OAuth tokens are refreshed and saved back.
func TokenSource(ctx context.Context, cfg config.AuthConfig, store tokenstore.Store) (*Source, error) {
	if pat := os.Getenv(EnvToken); pat != "" {
		return &Source{TokenSource: patSource(pat), Method: tokenstore.MethodPAT, FromEnv: true}, nil
	}
	creds, err := store.Load(ctx)
	if errors.Is(err, tokenstore.ErrNotFound) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	if creds.Method() == tokenstore.MethodPAT {
		return &Source{TokenSource: patSource(creds.PersonalAccessToken), Method: tokenstore.MethodPAT}, nil
	}
	if cfg.ClientID == "" {
		return nil, errors.New("auth.client_id が設定されていません（config.toml または ASANA_CLIENT_ID）。" +
			"OAuth アプリを登録しないで使うなら `asagao auth login --pat` で Personal Access Token を登録してください")
	}
	newBase := func(c *tokenstore.Credentials) oauth2.TokenSource {
		oc := OAuthConfig(cfg, c.ClientSecret, LocalRedirectURL(cfg.RedirectPort))
		return oc.TokenSource(context.WithoutCancel(ctx), &oauth2.Token{
			AccessToken:  c.AccessToken,
			RefreshToken: c.RefreshToken,
			Expiry:       c.Expiry,
			TokenType:    "Bearer",
		})
	}
	return &Source{Method: tokenstore.MethodOAuth, TokenSource: &persistingSource{
		ctx:     ctx,
		base:    newBase(creds),
		newBase: newBase,
		store:   store,
		creds:   *creds,
	}}, nil
}

// patSource never refreshes: a PAT has no expiry (zero Expiry means always valid).
func patSource(pat string) oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: pat, TokenType: "Bearer"})
}

// persistingSource saves refreshed tokens back to the store.
// For stores that don't cache access tokens (1Password), it writes only when Asana rotates the refresh token.
//
// Before writing, it reads the store again so a login made meanwhile in another process is
// never overwritten with ours: the same user's new login (a PAT, another OAuth grant) is
// adopted, so a running TUI or MCP server switches to it; another user's is not
// (ErrLoginReplaced); after a logout it reports ErrNotLoggedIn.
//
// Once a PAT is adopted the store is not read again (a PAT is never saved back): a later
// logout or login is noticed only when Asana rejects the PAT (401) or the process restarts.
type persistingSource struct {
	ctx   context.Context
	base  oauth2.TokenSource
	store tokenstore.Store
	// newBase builds the OAuth token source for credentials found in the store.
	newBase func(*tokenstore.Credentials) oauth2.TokenSource

	mu    sync.Mutex
	creds tokenstore.Credentials
}

// errLoginChanged means the store now holds another login, which has been adopted.
var errLoginChanged = errors.New("ログインが別の場所で変わりました。もう一度試してください")

func (p *persistingSource) Token() (*oauth2.Token, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	tok, err := p.token()
	if errors.Is(err, errLoginChanged) {
		tok, err = p.token() // with the login found in the store
	}
	return tok, err
}

func (p *persistingSource) token() (*oauth2.Token, error) {
	if p.creds.Method() == tokenstore.MethodPAT { // adopted a PAT: nothing to refresh or save
		return p.base.Token()
	}
	tok, err := p.base.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			// Our refresh token is no longer valid: use a login made meanwhile, if any.
			changed, lerr := p.reload()
			if errors.Is(lerr, ErrLoginReplaced) {
				return nil, lerr
			}
			if lerr != nil || !changed {
				return nil, ErrNotLoggedIn
			}
			return nil, errLoginChanged
		}
		return nil, err
	}
	rotated := tok.RefreshToken != "" && tok.RefreshToken != p.creds.RefreshToken
	refreshed := tok.AccessToken != p.creds.AccessToken
	if rotated || (refreshed && p.store.CachesAccessToken()) {
		if changed, err := p.reload(); err != nil {
			return nil, err
		} else if changed {
			return nil, errLoginChanged
		}
		next := p.creds
		if tok.RefreshToken != "" {
			next.RefreshToken = tok.RefreshToken
		}
		next.AccessToken = tok.AccessToken
		next.Expiry = tok.Expiry
		if !p.store.CachesAccessToken() {
			next.AccessToken, next.Expiry = "", time.Time{}
		}
		if err := p.store.Save(p.ctx, &next); err != nil {
			return nil, err
		}
		p.creds = next
	}
	// Remember the in-memory access token so we don't re-save on every call.
	p.creds.AccessToken = tok.AccessToken
	p.creds.Expiry = tok.Expiry
	return tok, nil
}

// reload reads the stored credentials. If they are another login of the same user (a PAT,
// or another refresh token), it switches to them and reports changed. Another or an
// unknown user is ErrLoginReplaced; nothing stored (a logout) is ErrNotLoggedIn.
func (p *persistingSource) reload() (changed bool, err error) {
	cur, err := p.store.Load(p.ctx)
	if errors.Is(err, tokenstore.ErrNotFound) {
		return false, ErrNotLoggedIn
	}
	if err != nil {
		return false, err
	}
	if cur.Method() == tokenstore.MethodOAuth && cur.RefreshToken == p.creds.RefreshToken {
		return false, nil
	}
	if cur.UserGID == "" || cur.UserGID != p.creds.UserGID {
		return false, ErrLoginReplaced
	}
	p.creds = *cur
	if cur.Method() == tokenstore.MethodPAT {
		p.base = patSource(cur.PersonalAccessToken)
	} else {
		p.base = p.newBase(cur)
	}
	return true, nil
}
