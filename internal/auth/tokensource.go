package auth

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/auth/tokenstore"
	"github.com/amasok/asana-tui/internal/config"
)

// ErrNotLoggedIn is returned when neither ASANA_TOKEN nor stored credentials exist.
var ErrNotLoggedIn = errors.New("ログインしていません。`asana-tui auth login --pat` で Personal Access Token（" + PATConsoleURL +
	" で作成）を登録するか、`asana-tui auth login` で OAuth ログインしてください")

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
		return "保存した Personal Access Token が無効です。`asana-tui auth login --pat` で新しいトークン（" + PATConsoleURL + " で作成）を登録してください"
	}
	return "ログインが無効になっています。`asana-tui auth login` でログインし直してください"
}

// LoginHelp returns what the user should do when err means they must log in (again):
// nothing stored, or a token Asana rejects. It returns "" for any other error.
func LoginHelp(err error) string {
	var apiErr *asana.Error
	switch {
	case errors.Is(err, ErrNotLoggedIn):
		return ErrNotLoggedIn.Error()
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized:
		if apiErr.Hint != "" {
			return apiErr.Hint
		}
		return "Asana がトークンを受け付けません。`asana-tui auth login --pat` か `asana-tui auth login` でログインし直してください"
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
			"OAuth アプリを登録しないで使うなら `asana-tui auth login --pat` で Personal Access Token を登録してください")
	}
	oc := OAuthConfig(cfg, creds.ClientSecret, LocalRedirectURL(cfg.RedirectPort))
	initial := &oauth2.Token{
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		Expiry:       creds.Expiry,
		TokenType:    "Bearer",
	}
	return &Source{Method: tokenstore.MethodOAuth, TokenSource: &persistingSource{
		ctx:   ctx,
		base:  oc.TokenSource(context.WithoutCancel(ctx), initial),
		store: store,
		creds: *creds,
	}}, nil
}

// patSource never refreshes: a PAT has no expiry (zero Expiry means always valid).
func patSource(pat string) oauth2.TokenSource {
	return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: pat, TokenType: "Bearer"})
}

// persistingSource saves refreshed tokens back to the store.
// For stores that don't cache access tokens (1Password), it writes only when Asana rotates the refresh token.
type persistingSource struct {
	ctx   context.Context
	base  oauth2.TokenSource
	store tokenstore.Store

	mu    sync.Mutex
	creds tokenstore.Credentials
}

func (p *persistingSource) Token() (*oauth2.Token, error) {
	tok, err := p.base.Token()
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
			return nil, ErrNotLoggedIn
		}
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	rotated := tok.RefreshToken != "" && tok.RefreshToken != p.creds.RefreshToken
	refreshed := tok.AccessToken != p.creds.AccessToken
	if rotated || (refreshed && p.store.CachesAccessToken()) {
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
