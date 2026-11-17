// Package tokenstore persists credentials (OAuth tokens or a Personal Access Token) in the
// OS keychain, 1Password or a file.
package tokenstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/config"
)

// ErrNotFound means no credentials have been saved yet (not logged in).
var ErrNotFound = errors.New("credentials not found")

// Credentials is what a login produces and what later runs need to get an access token.
// An OAuth login stores a refresh token; a PAT login stores PersonalAccessToken instead
// (it never expires and is never refreshed).
type Credentials struct {
	// PersonalAccessToken is set for a PAT login (then RefreshToken is empty).
	PersonalAccessToken string `json:"personal_access_token,omitempty"`
	// ClientSecret is kept across a switch to PAT, so going back to OAuth needs no re-entry.
	ClientSecret string    `json:"client_secret,omitempty"`
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	UserGID      string    `json:"user_gid,omitempty"`
	UserName     string    `json:"user_name,omitempty"`
	UserEmail    string    `json:"user_email,omitempty"`
}

// Method is how the credentials were obtained.
type Method string

const (
	MethodOAuth Method = "oauth"
	MethodPAT   Method = "personal_access_token"
)

// Method reports whether these are OAuth or PAT credentials.
func (c *Credentials) Method() Method {
	if c.PersonalAccessToken != "" {
		return MethodPAT
	}
	return MethodOAuth
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

// keychainService is the keychain service the login is saved under (it was "asana-tui"
// before the rename; that one is still read, see withLegacy).
const keychainService = "asagao"

// New returns the store cfg chooses. The keychain, and 1Password with the default item name,
// also read a login saved under the former name (asana-tui) when none is saved yet.
func New(cfg config.AuthConfig) (Store, error) {
	switch cfg.TokenStore {
	case config.TokenStoreKeychain:
		return newWithLegacy(
			&Keychain{Service: keychainService, User: "default"},
			&Keychain{Service: config.LegacyAppName, User: "default"},
		), nil
	case config.TokenStore1Password:
		op := &OnePassword{Vault: cfg.OnePassword.Vault, Item: cfg.OnePassword.Item, Account: cfg.OnePassword.Account}
		if l := cfg.OnePassword.LegacyItem; l != "" && l != op.Item {
			return newWithLegacy(op, &OnePassword{Vault: op.Vault, Item: l, Account: op.Account}), nil
		}
		return op, nil
	case config.TokenStoreFile:
		return &File{Path: defaultFilePath()}, nil
	}
	return nil, fmt.Errorf("unknown token store %q", cfg.TokenStore)
}
