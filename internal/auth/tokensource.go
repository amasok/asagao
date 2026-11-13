package auth

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/amasok/asana-tui/internal/auth/tokenstore"
	"github.com/amasok/asana-tui/internal/config"
)

// ErrNotLoggedIn is returned when neither ASANA_TOKEN nor stored credentials exist.
var ErrNotLoggedIn = errors.New("ログインしていません。`asana-tui auth login` を実行してください")

// EnvToken is the environment variable for a Personal Access Token (fallback for dev / headless).
const EnvToken = "ASANA_TOKEN"

// TokenSource returns a token source for API calls.
// ASANA_TOKEN (a Personal Access Token) takes precedence over OAuth credentials.
func TokenSource(ctx context.Context, cfg config.AuthConfig, store tokenstore.Store) (oauth2.TokenSource, error) {
	if pat := os.Getenv(EnvToken); pat != "" {
		return oauth2.StaticTokenSource(&oauth2.Token{AccessToken: pat, TokenType: "Bearer"}), nil
	}
	if cfg.ClientID == "" {
		return nil, errors.New("auth.client_id が設定されていません（config.toml または ASANA_CLIENT_ID）")
	}
	creds, err := store.Load(ctx)
	if errors.Is(err, tokenstore.ErrNotFound) {
		return nil, ErrNotLoggedIn
	}
	if err != nil {
		return nil, err
	}
	oc := OAuthConfig(cfg, creds.ClientSecret, LocalRedirectURL(cfg.RedirectPort))
	initial := &oauth2.Token{
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		Expiry:       creds.Expiry,
		TokenType:    "Bearer",
	}
	return &persistingSource{
		ctx:   ctx,
		base:  oc.TokenSource(context.WithoutCancel(ctx), initial),
		store: store,
		creds: *creds,
	}, nil
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
