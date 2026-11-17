package tokenstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/amasok/asagao/internal/config"
)

func TestNew(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/conf")
	for _, tc := range []struct {
		store, name string
		caches      bool
	}{
		{config.TokenStoreKeychain, "keychain", true},
		{config.TokenStore1Password, "1password", false},
		{config.TokenStoreFile, "file", true},
	} {
		cfg := config.Default().Auth
		cfg.TokenStore = tc.store
		cfg.OnePassword = config.OnePasswordConfig{Vault: "V", Item: "I", Account: "A"}
		s, err := New(cfg)
		if err != nil {
			t.Fatalf("New(%q): %v", tc.store, err)
		}
		if s.Name() != tc.name || s.CachesAccessToken() != tc.caches {
			t.Errorf("New(%q) = %s caches=%v, want %s caches=%v", tc.store, s.Name(), s.CachesAccessToken(), tc.name, tc.caches)
		}
		switch s := s.(type) {
		case *Keychain:
			if s.Service != "asagao" || s.User != "default" {
				t.Errorf("keychain = %+v", s)
			}
		case *OnePassword:
			if s.Vault != "V" || s.Item != "I" || s.Account != "A" {
				t.Errorf("1password = %+v", s)
			}
		case *File:
			if want := filepath.Join("/x/conf", "asagao", "token.json"); s.Path != want {
				t.Errorf("file path = %q, want %q", s.Path, want)
			}
		}
	}
	if _, err := New(config.AuthConfig{TokenStore: "vault"}); err == nil {
		t.Fatal("unknown store should fail")
	}
}

func TestKeychain(t *testing.T) {
	keyring.MockInit()
	ctx := context.Background()
	k := &Keychain{Service: "asagao-test", User: "u"}
	if _, err := k.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load before save: %v, want ErrNotFound", err)
	}
	exp := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	in := &Credentials{ClientSecret: "sec", RefreshToken: "rt", AccessToken: "at", Expiry: exp, UserGID: "9", UserName: "K", UserEmail: "k@example.com"}
	if err := k.Save(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := k.Load(ctx)
	if err != nil || *got != *in {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, in)
	}
	// Another service/user is a different secret.
	if _, err := (&Keychain{Service: "asagao-test", User: "other"}).Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other user: %v", err)
	}
	if err := k.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Load(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load after delete: %v", err)
	}
	if err := k.Delete(ctx); err != nil {
		t.Fatalf("deleting a missing secret should succeed: %v", err)
	}

	// A broken secret is an error, not "not logged in".
	if err := keyring.Set("asagao-test", "u", "{not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Load(ctx); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("broken secret: %v", err)
	}
}

func TestKeychainErrors(t *testing.T) {
	locked := errors.New("keychain locked")
	keyring.MockInitWithError(locked)
	t.Cleanup(keyring.MockInit)
	ctx := context.Background()
	k := &Keychain{Service: "s", User: "u"}
	if _, err := k.Load(ctx); !errors.Is(err, locked) {
		t.Errorf("Load: %v", err)
	}
	if err := k.Save(ctx, &Credentials{}); !errors.Is(err, locked) {
		t.Errorf("Save: %v", err)
	}
	if err := k.Delete(ctx); !errors.Is(err, locked) {
		t.Errorf("Delete: %v", err)
	}
}

func TestFileStoreDetails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	f := &File{Path: filepath.Join(dir, "a", "token.json")}
	if err := f.Save(ctx, &Credentials{RefreshToken: "rt1"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(f.Path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, want 0600", info.Mode().Perm())
	}
	if dirInfo, _ := os.Stat(filepath.Dir(f.Path)); dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v, want 0700", dirInfo.Mode().Perm())
	}
	if err := f.Save(ctx, &Credentials{RefreshToken: "rt2"}); err != nil {
		t.Fatal(err)
	}
	if c, err := f.Load(ctx); err != nil || c.RefreshToken != "rt2" {
		t.Fatalf("overwrite: %+v, %v", c, err)
	}
	if _, err := os.Stat(f.Path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp file left behind: %v", err)
	}
	if err := os.WriteFile(f.Path, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(ctx); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("broken file: %v", err)
	}
	// A directory where the file should be: Load and Save fail (not ErrNotFound).
	bad := &File{Path: dir}
	if _, err := bad.Load(ctx); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Load of a directory: %v", err)
	}
	if err := bad.Save(ctx, &Credentials{}); err == nil {
		t.Fatal("Save over a directory should fail")
	}
	if err := (&File{Path: filepath.Join(dir, "missing.json")}).Delete(ctx); err != nil {
		t.Fatalf("deleting a missing file should succeed: %v", err)
	}
}
