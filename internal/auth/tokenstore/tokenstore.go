// Package tokenstore persists OAuth credentials in the OS keychain, 1Password or a file.
package tokenstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asana-tui/internal/config"
)

// ErrNotFound means no credentials have been saved yet (not logged in).
var ErrNotFound = errors.New("credentials not found")

// Credentials is what a login produces and what later runs need to get an access token.
type Credentials struct {
	ClientSecret string    `json:"client_secret,omitempty"`
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	UserGID      string    `json:"user_gid,omitempty"`
	UserName     string    `json:"user_name,omitempty"`
	UserEmail    string    `json:"user_email,omitempty"`
}

type Store interface {
	Name() string
	Load(ctx context.Context) (*Credentials, error)
	Save(ctx context.Context, c *Credentials) error
	Delete(ctx context.Context) error
	// CachesAccessToken reports whether Save persists the short-lived access token.
	// Stores that return false are only written when the refresh token or secret changes.
	CachesAccessToken() bool
}

func New(cfg config.AuthConfig) (Store, error) {
	switch cfg.TokenStore {
	case config.TokenStoreKeychain:
		return &Keychain{Service: "asana-tui", User: "default"}, nil
	case config.TokenStore1Password:
		return &OnePassword{Vault: cfg.OnePassword.Vault, Item: cfg.OnePassword.Item, Account: cfg.OnePassword.Account}, nil
	case config.TokenStoreFile:
		return &File{Path: defaultFilePath()}, nil
	}
	return nil, fmt.Errorf("unknown token store %q", cfg.TokenStore)
}
