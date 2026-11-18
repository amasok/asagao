package tokenstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// In a test binary the keychain is go-keyring's in-memory mock, before any TestMain or test
// runs: no test reads or writes the user's real entries (services asagao and asana-tui), and
// an OAuth token found there is never refreshed against the real Asana (Issue #53).
func init() {
	if testing.Testing() {
		keyring.MockInit()
		keychainMocked = true
	}
}

// keychainMocked is whether init put the mock in place (for its test).
var keychainMocked bool

// Keychain stores credentials as one JSON secret in macOS Keychain / Linux Secret Service.
type Keychain struct {
	Service string
	User    string
}

func (k *Keychain) Name() string            { return "keychain" }
func (k *Keychain) CachesAccessToken() bool { return true }

func (k *Keychain) Load(context.Context) (*Credentials, error) {
	s, err := keyring.Get(k.Service, k.User)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (k *Keychain) Save(_ context.Context, c *Credentials) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return keyring.Set(k.Service, k.User, string(b))
}

func (k *Keychain) Delete(context.Context) error {
	err := keyring.Delete(k.Service, k.User)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
