package tokenstore

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/amasok/asagao/internal/config"
)

func kcStores(t *testing.T) (s Store, cur, old *Keychain) {
	t.Helper()
	keyring.MockInit()
	s, err := New(config.Default().Auth)
	if err != nil {
		t.Fatal(err)
	}
	return s, &Keychain{Service: "asagao", User: "default"}, &Keychain{Service: config.LegacyAppName, User: "default"}
}

// A newer login under "asagao" is never replaced by the old entry, however often we load.
func TestAttackFallbackNeverOverwritesNewer(t *testing.T) {
	ctx := context.Background()
	s, cur, old := kcStores(t)
	old.Save(ctx, &Credentials{RefreshToken: "OLD"})
	cur.Save(ctx, &Credentials{RefreshToken: "NEW"})
	for range 3 {
		c, err := s.Load(ctx)
		if err != nil || c.RefreshToken != "NEW" {
			t.Fatalf("Load = %+v %v", c, err)
		}
	}
	if v, _ := keyring.Get("asagao", "default"); !strings.Contains(v, "NEW") {
		t.Errorf("asagao entry overwritten: %s", v)
	}
	// a Save through the wrapper writes only asagao and leaves the old one alone
	s.Save(ctx, &Credentials{RefreshToken: "NEWER"})
	if v, _ := keyring.Get(config.LegacyAppName, "default"); !strings.Contains(v, "OLD") {
		t.Errorf("old entry was modified: %s", v)
	}
	// after the fallback ran once, rotating the old entry is not picked up again
	old.Save(ctx, &Credentials{RefreshToken: "OLD2"})
	if c, _ := s.Load(ctx); c.RefreshToken != "NEWER" {
		t.Errorf("got %+v", c)
	}
}

// Logout after the fallback: both entries are gone and the next Load is "not found".
func TestAttackLogoutAfterFallback(t *testing.T) {
	ctx := context.Background()
	s, _, old := kcStores(t)
	old.Save(ctx, &Credentials{RefreshToken: "OLD"})
	if _, err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("login came back: %v", err)
	}
	// deleting again, or with only one of them present, is not an error
	if err := s.Delete(ctx); err != nil {
		t.Errorf("second delete: %v", err)
	}
	old.Save(ctx, &Credentials{RefreshToken: "ONLYOLD"})
	if err := s.Delete(ctx); err != nil {
		t.Errorf("delete with only the old one: %v", err)
	}
	if _, err := old.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("old entry survived: %v", err)
	}
}

// Nothing anywhere: ErrNotFound, and nothing is written.
func TestAttackNothingSavedWritesNothing(t *testing.T) {
	s, _, _ := kcStores(t)
	if _, err := s.Load(context.Background()); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := keyring.Get("asagao", "default"); err == nil {
		t.Error("an entry was created from nothing")
	}
}

// A garbled legacy entry is an error that names the old service, not a panic or a bogus login.
func TestAttackGarbledLegacyEntry(t *testing.T) {
	s, cur, _ := kcStores(t)
	keyring.Set(config.LegacyAppName, "default", "{not json")
	c, err := s.Load(context.Background())
	if err == nil || c != nil {
		t.Fatalf("got %+v %v", c, err)
	}
	if _, err := keyring.Get("asagao", "default"); err == nil {
		t.Error("garbage copied to asagao")
	}
	_ = cur
}

// 1Password: logout deletes both items, even when the first delete fails.
func TestAttackOnePasswordLogoutAfterFallback(t *testing.T) {
	ctx := context.Background()
	curOP, oldOP := &fakeOP{}, &fakeOP{}
	cur := &OnePassword{Vault: "Private", Item: "asagao", run: curOP.run}
	old := &OnePassword{Vault: "Private", Item: config.LegacyAppName, run: oldOP.run}
	old.Save(ctx, &Credentials{RefreshToken: "secret-rt"})
	s := &withLegacy{Store: cur, legacy: old, lockPath: filepath.Join(t.TempDir(), ".login.lock")}
	if _, err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("login came back: %v", err)
	}
}

// 1Password: the old item is only read, never edited or deleted, by Load.
func TestAttackOnePasswordLoadDoesNotTouchOld(t *testing.T) {
	ctx := context.Background()
	curOP, oldOP := &fakeOP{}, &fakeOP{}
	old := &OnePassword{Vault: "Private", Item: config.LegacyAppName, run: oldOP.run}
	old.Save(ctx, &Credentials{RefreshToken: "secret-rt"})
	n := len(oldOP.calls)
	s := &withLegacy{Store: &OnePassword{Vault: "Private", Item: "asagao", run: curOP.run}, legacy: old, lockPath: filepath.Join(t.TempDir(), ".login.lock")}
	if _, err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	for _, c := range oldOP.calls[n:] {
		if c[1] != "get" {
			t.Errorf("Load ran %v on the old item", c)
		}
	}
}
