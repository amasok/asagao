package tokenstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/amasok/asagao/internal/config"
)

// A login saved in the keychain before the rename (service "asana-tui") is read when none
// is saved under "asagao", and saved again under "asagao". The old entry is kept: an old
// binary may still use it.
func TestKeychainFallsBackToLegacyService(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	old := &Keychain{Service: config.LegacyAppName, User: "default"}
	if err := old.Save(ctx, &Credentials{PersonalAccessToken: "old-pat", UserGID: "7"}); err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Default().Auth)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name() != "keychain" || !s.CachesAccessToken() {
		t.Errorf("name %s caches %v", s.Name(), s.CachesAccessToken())
	}
	c, err := s.Load(ctx)
	if err != nil || c.PersonalAccessToken != "old-pat" || c.UserGID != "7" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	if v, err := keyring.Get("asagao", "default"); err != nil || !strings.Contains(v, "old-pat") {
		t.Errorf("the login should be saved again under asagao: %q, %v", v, err)
	}
	if v, err := keyring.Get(config.LegacyAppName, "default"); err != nil || !strings.Contains(v, "old-pat") {
		t.Errorf("the old entry should be kept: %q, %v", v, err)
	}
	// A new login is saved under asagao only, and read from there from now on.
	if err := s.Save(ctx, &Credentials{PersonalAccessToken: "new-pat"}); err != nil {
		t.Fatal(err)
	}
	if c, err := s.Load(ctx); err != nil || c.PersonalAccessToken != "new-pat" {
		t.Fatalf("Load after save = %+v, %v", c, err)
	}
	if v, _ := keyring.Get(config.LegacyAppName, "default"); !strings.Contains(v, "old-pat") {
		t.Errorf("Save should not touch the old entry: %q", v)
	}
	// Logout forgets both: otherwise the next Load would bring the old one back.
	if err := s.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after Delete: %v", err)
	}
}

func TestKeychainNothingSaved(t *testing.T) {
	keyring.MockInit()
	s, err := New(config.Default().Auth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound: %v", err)
	}
	if _, err := keyring.Get("asagao", "default"); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("nothing should be saved: %v", err)
	}
}

// memStore is a store in memory whose Load and Save can fail.
type memStore struct {
	c                *Credentials
	loadErr, saveErr error
	saves, deletes   int
}

func (m *memStore) Name() string            { return "mem" }
func (m *memStore) CachesAccessToken() bool { return true }
func (m *memStore) Load(context.Context) (*Credentials, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.c == nil {
		return nil, ErrNotFound
	}
	return m.c, nil
}
func (m *memStore) Save(_ context.Context, c *Credentials) error {
	m.saves++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.c = c
	return nil
}
func (m *memStore) Delete(context.Context) error { m.deletes++; m.c = nil; return nil }

func TestWithLegacyEdgeCases(t *testing.T) {
	ctx := context.Background()
	t.Run("current wins", func(t *testing.T) {
		cur, old := &memStore{c: &Credentials{RefreshToken: "new"}}, &memStore{c: &Credentials{RefreshToken: "old"}}
		c, err := (&withLegacy{Store: cur, legacy: old}).Load(ctx)
		if err != nil || c.RefreshToken != "new" || cur.saves != 0 {
			t.Fatalf("%+v, %v, saves %d", c, err, cur.saves)
		}
	})
	t.Run("current fails: no fallback", func(t *testing.T) {
		boom := errors.New("keychain locked")
		cur, old := &memStore{loadErr: boom}, &memStore{c: &Credentials{RefreshToken: "old"}}
		if _, err := (&withLegacy{Store: cur, legacy: old}).Load(ctx); !errors.Is(err, boom) {
			t.Fatalf("want the current store's error: %v", err)
		}
	})
	t.Run("old fails: says which", func(t *testing.T) {
		boom := errors.New("op not signed in")
		_, err := (&withLegacy{Store: &memStore{}, legacy: &memStore{loadErr: boom}}).Load(ctx)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), config.LegacyAppName) {
			t.Fatalf("want a wrapped error naming %s: %v", config.LegacyAppName, err)
		}
	})
	t.Run("copy fails: login still works", func(t *testing.T) {
		cur := &memStore{saveErr: errors.New("read-only")}
		c, err := (&withLegacy{Store: cur, legacy: &memStore{c: &Credentials{RefreshToken: "old"}}}).Load(ctx)
		if err != nil || c.RefreshToken != "old" || cur.saves != 1 {
			t.Fatalf("%+v, %v, saves %d", c, err, cur.saves)
		}
	})
}

// 1Password with the default item name falls back to the item "asana-tui" in the same
// vault and account, and copies it to "asagao" without deleting the old item.
func TestOnePasswordFallsBackToLegacyItem(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default().Auth
	cfg.TokenStore = config.TokenStore1Password
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w, ok := s.(*withLegacy)
	if !ok {
		t.Fatalf("default item should fall back: %T", s)
	}
	cur, old := w.Store.(*OnePassword), w.legacy.(*OnePassword)
	if cur.Item != "asagao" || old.Item != config.LegacyAppName || old.Vault != cur.Vault || old.Account != cur.Account {
		t.Fatalf("items: %+v / %+v", cur, old)
	}
	curOP, oldOP := &fakeOP{}, &fakeOP{}
	cur.run, old.run = curOP.run, oldOP.run
	if err := (&OnePassword{Vault: "Private", Item: config.LegacyAppName, run: oldOP.run}).Save(ctx,
		&Credentials{ClientSecret: "secret-cs", RefreshToken: "secret-rt", UserGID: "9"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.Load(ctx)
	if err != nil || c.RefreshToken != "secret-rt" || c.ClientSecret != "secret-cs" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
	if curOP.item == nil {
		t.Fatal("the login should be saved again in the asagao item")
	}
	if c, err := cur.Load(ctx); err != nil || c.RefreshToken != "secret-rt" {
		t.Fatalf("asagao item = %+v, %v", c, err)
	}
	if oldOP.item == nil {
		t.Error("the old item should be kept")
	}
}

// An item name set in config.toml is kept as it is: no fallback.
func TestOnePasswordExplicitItemHasNoFallback(t *testing.T) {
	for _, item := range []string{"my-asana", "asagao", config.LegacyAppName} {
		cfg := config.Default().Auth
		cfg.TokenStore = config.TokenStore1Password
		cfg.OnePassword.Item, cfg.OnePassword.LegacyItem = item, ""
		s, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		op, ok := s.(*OnePassword)
		if !ok || op.Item != item {
			t.Errorf("item %q: got %T %+v", item, s, s)
		}
	}
}
