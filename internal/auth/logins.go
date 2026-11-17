package auth

import (
	"context"
	"fmt"

	"github.com/amasok/asagao/internal/auth/tokenstore"
	"github.com/amasok/asagao/internal/config"
)

// The saved login, as the CLI's auth commands use it: they reach the token store only
// through auth (architecture.md, 依存の決まり 4).

// TokenStore is where a login is saved (keychain, 1Password or a file).
type TokenStore = tokenstore.Store

// Credentials is a saved login.
type Credentials = tokenstore.Credentials

// Method is how a saved login authenticates.
type Method = tokenstore.Method

// MethodPAT is a login with a Personal Access Token.
const MethodPAT = tokenstore.MethodPAT

// ErrNoSavedLogin is returned by TokenStore.Load when nothing is saved.
var ErrNoSavedLogin = tokenstore.ErrNotFound

// OpenTokenStore is the token store cfg names.
func OpenTokenStore(cfg config.AuthConfig) (TokenStore, error) { return tokenstore.New(cfg) }

// MoveLogin moves the saved login from one token store to another, and returns the store
// it was moved to. An access token is not saved where the store does not cache one.
func MoveLogin(ctx context.Context, from TokenStore, cfg config.AuthConfig, to string) (TokenStore, error) {
	cfg.TokenStore = to
	dst, err := tokenstore.New(cfg)
	if err != nil {
		return nil, err
	}
	if dst.Name() == from.Name() {
		return nil, fmt.Errorf("移行元と移行先が同じです (%s)", to)
	}
	c, err := from.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("%s からの読み込み: %w", from.Name(), err)
	}
	if !dst.CachesAccessToken() {
		c.AccessToken = ""
	}
	if err := dst.Save(ctx, c); err != nil {
		return nil, fmt.Errorf("%s への保存: %w", dst.Name(), err)
	}
	if err := from.Delete(ctx); err != nil {
		return nil, fmt.Errorf("%s からの削除: %w", from.Name(), err)
	}
	return dst, nil
}
