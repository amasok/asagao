package tokenstore

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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
	mu               sync.Mutex
	c                *Credentials
	loadErr, saveErr error
	saves, deletes   int
}

func (m *memStore) Name() string            { return "mem" }
func (m *memStore) CachesAccessToken() bool { return true }
func (m *memStore) Load(context.Context) (*Credentials, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.c == nil {
		return nil, ErrNotFound
	}
	return m.c, nil
}
func (m *memStore) Save(_ context.Context, c *Credentials) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saves++
	if m.saveErr != nil {
		return m.saveErr
	}
	m.c = c
	return nil
}
func (m *memStore) Delete(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes++
	m.c = nil
	return nil
}

func TestWithLegacyEdgeCases(t *testing.T) {
	ctx := context.Background()
	t.Run("current wins", func(t *testing.T) {
		cur, old := &memStore{c: &Credentials{RefreshToken: "new"}}, &memStore{c: &Credentials{RefreshToken: "old"}}
		c, err := (&withLegacy{Store: cur, legacy: old, lockPath: lockPath(t)}).Load(ctx)
		if err != nil || c.RefreshToken != "new" || cur.saves != 0 {
			t.Fatalf("%+v, %v, saves %d", c, err, cur.saves)
		}
	})
	t.Run("current fails: no fallback", func(t *testing.T) {
		boom := errors.New("keychain locked")
		cur, old := &memStore{loadErr: boom}, &memStore{c: &Credentials{RefreshToken: "old"}}
		if _, err := (&withLegacy{Store: cur, legacy: old, lockPath: lockPath(t)}).Load(ctx); !errors.Is(err, boom) {
			t.Fatalf("want the current store's error: %v", err)
		}
	})
	t.Run("old fails: says which", func(t *testing.T) {
		boom := errors.New("op not signed in")
		_, err := (&withLegacy{Store: &memStore{}, legacy: &memStore{loadErr: boom}, lockPath: lockPath(t)}).Load(ctx)
		if !errors.Is(err, boom) || !strings.Contains(err.Error(), config.LegacyAppName) {
			t.Fatalf("want a wrapped error naming %s: %v", config.LegacyAppName, err)
		}
	})
	t.Run("copy fails: login still works", func(t *testing.T) {
		cur := &memStore{saveErr: errors.New("read-only")}
		c, err := (&withLegacy{Store: cur, legacy: &memStore{c: &Credentials{RefreshToken: "old"}}, lockPath: lockPath(t)}).Load(ctx)
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

func lockPath(t *testing.T) string { return filepath.Join(t.TempDir(), ".login.lock") }

// fakeVault is a 1Password vault where, as in 1Password, titles are not unique: a create
// always adds an item, and a get of a title two items have fails ("More than one item").
// get and create take a while, as `op` does.
type fakeVault struct {
	mu      sync.Mutex
	items   map[string][]map[string]any
	creates int
}

func (v *fakeVault) run(_ context.Context, stdin []byte, args ...string) ([]byte, error) {
	time.Sleep(20 * time.Millisecond)
	v.mu.Lock()
	defer v.mu.Unlock()
	switch args[1] {
	case "get":
		switch its := v.items[args[2]]; len(its) {
		case 0:
			return nil, &opError{msg: `[ERROR] "` + args[2] + `" isn't an item in the "Private" vault.`}
		case 1:
			return json.Marshal(its[0])
		default:
			return nil, &opError{msg: `[ERROR] More than one item matches "` + args[2] + `".`}
		}
	case "create":
		var it map[string]any
		if err := json.Unmarshal(stdin, &it); err != nil {
			return nil, err
		}
		title, _ := it["title"].(string)
		it["id"] = title + "-id"
		v.items[title] = append(v.items[title], it)
		v.creates++
		return nil, nil
	}
	panic("unexpected op call " + strings.Join(args, " "))
}

// The TUI and an MCP server start together after the rename, both finding only the old
// 1Password item: the login is copied once, so there is one "asagao" item and later runs
// can read it.
func TestOnePasswordLegacyCopyOnceAcrossProcesses(t *testing.T) {
	ctx := context.Background()
	v := &fakeVault{items: map[string][]map[string]any{}}
	if err := (&OnePassword{Vault: "Private", Item: config.LegacyAppName, run: v.run}).Save(ctx,
		&Credentials{RefreshToken: "secret-rt", ClientSecret: "secret-cs"}); err != nil {
		t.Fatal(err)
	}
	v.creates = 0
	lock := lockPath(t)
	var wg sync.WaitGroup
	for range 4 {
		// one store per "process", sharing the vault and the lock file
		s := &withLegacy{
			Store:    &OnePassword{Vault: "Private", Item: "asagao", run: v.run},
			legacy:   &OnePassword{Vault: "Private", Item: config.LegacyAppName, run: v.run},
			lockPath: lock,
		}
		wg.Go(func() {
			if c, err := s.Load(ctx); err != nil || c.RefreshToken != "secret-rt" {
				t.Errorf("Load = %+v, %v", c, err)
			}
		})
	}
	wg.Wait()
	if v.creates != 1 || len(v.items["asagao"]) != 1 {
		t.Fatalf("want one asagao item, got %d (creates %d)", len(v.items["asagao"]), v.creates)
	}
	if c, err := (&OnePassword{Vault: "Private", Item: "asagao", run: v.run}).Load(ctx); err != nil || c.RefreshToken != "secret-rt" {
		t.Fatalf("asagao item afterwards: %+v, %v", c, err)
	}
}

// A login saved under the new name while a copy waits for the lock is not overwritten by
// the copy.
func TestLegacyCopyDoesNotOverwriteNewLogin(t *testing.T) {
	ctx := context.Background()
	lock := lockPath(t)
	cur := &memStore{}
	s := &withLegacy{Store: cur, legacy: &memStore{c: &Credentials{RefreshToken: "old"}}, lockPath: lock}
	unlock, err := lockCopy(ctx, lock) // another process holds the lock...
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *Credentials)
	go func() { c, _ := s.Load(ctx); done <- c }()
	time.Sleep(100 * time.Millisecond)
	_ = cur.Save(ctx, &Credentials{RefreshToken: "new"}) // ...and saves a new login
	unlock()
	if c := <-done; c.RefreshToken != "new" {
		t.Errorf("Load = %+v, want the new login", c)
	}
	if c, _ := cur.Load(ctx); c.RefreshToken != "new" {
		t.Errorf("the new login was overwritten: %+v", c)
	}
}
