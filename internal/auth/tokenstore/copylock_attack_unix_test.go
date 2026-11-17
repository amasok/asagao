//go:build unix

package tokenstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// dirStore is a Store kept in a file, so several processes share it. Save is slow and
// logs every call, to catch a second create.
type dirStore struct{ dir, name string }

func (d *dirStore) Name() string            { return "dir" }
func (d *dirStore) CachesAccessToken() bool { return true }
func (d *dirStore) Load(context.Context) (*Credentials, error) {
	b, err := os.ReadFile(filepath.Join(d.dir, d.name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var c Credentials
	return &c, json.Unmarshal(b, &c)
}
func (d *dirStore) Save(_ context.Context, c *Credentials) error {
	f, _ := os.OpenFile(filepath.Join(d.dir, d.name+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	f.WriteString(c.RefreshToken + "\n")
	f.Close()
	time.Sleep(150 * time.Millisecond)
	b, _ := json.Marshal(c)
	return os.WriteFile(filepath.Join(d.dir, d.name), b, 0o600)
}
func (d *dirStore) Delete(context.Context) error { return os.Remove(filepath.Join(d.dir, d.name)) }

func helperStore(dir string) *withLegacy {
	return &withLegacy{
		Store:    &dirStore{dir, "cur"},
		legacy:   &dirStore{dir, "old"},
		lockPath: filepath.Join(dir, "cfg", ".login.lock"),
	}
}

func TestCopyLockHelperProcess(t *testing.T) {
	dir := os.Getenv("ASAGAO_LOCK_HELPER")
	if dir == "" {
		t.Skip("helper")
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(filepath.Join(dir, "go")); err == nil {
			break
		}
	}
	s := helperStore(dir)
	if v := os.Getenv("ASAGAO_LOCK_SAVE"); v != "" {
		if err := s.Save(context.Background(), &Credentials{RefreshToken: v}); err != nil {
			os.Exit(3)
		}
		return
	}
	c, err := s.Load(context.Background())
	if err != nil || (c.RefreshToken != "OLD" && c.RefreshToken != "NEW") {
		os.Exit(4)
	}
}

func runHelpers(t *testing.T, dir string, saveVal string, n int) {
	t.Helper()
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCopyLockHelperProcess$")
			cmd.Env = append(os.Environ(), "ASAGAO_LOCK_HELPER="+dir)
			if saveVal != "" && i == 0 {
				cmd.Env = append(cmd.Env, "ASAGAO_LOCK_SAVE="+saveVal)
			}
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("proc %d: %v %s", i, err, b)
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "go"), nil, 0o600)
	wg.Wait()
}

// Real processes copying a legacy login at once: exactly one create.
func TestAttackCopyOnceAcrossProcesses(t *testing.T) {
	for run := range 3 {
		dir := t.TempDir()
		(&dirStore{dir, "old"}).Save(context.Background(), &Credentials{RefreshToken: "OLD"})
		os.Remove(filepath.Join(dir, "old.log"))
		runHelpers(t, dir, "", 6)
		b, _ := os.ReadFile(filepath.Join(dir, "cur.log"))
		if n := len(strings.Fields(string(b))); n != 1 {
			t.Fatalf("run %d: %d creates: %q", run, n, b)
		}
	}
}

// A login saved while another process copies the old one is not overwritten by the copy.
func TestAttackCopyDoesNotOverwriteConcurrentSave(t *testing.T) {
	for run := range 3 {
		dir := t.TempDir()
		(&dirStore{dir, "old"}).Save(context.Background(), &Credentials{RefreshToken: "OLD"})
		runHelpers(t, dir, "NEW", 3)
		c, err := (&dirStore{dir, "cur"}).Load(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// the saver ran either before the copy (copy sees it, does not copy) or after (overwrites with NEW)
		if c.RefreshToken != "NEW" {
			t.Fatalf("run %d: current login is %q, the new login was lost", run, c.RefreshToken)
		}
	}
}

func legacyWith(t *testing.T, lock string) (*withLegacy, *memStore, *memStore) {
	cur, old := &memStore{}, &memStore{c: &Credentials{RefreshToken: "OLD"}}
	return &withLegacy{Store: cur, legacy: old, lockPath: lock}, cur, old
}

// The lock file cannot be made (the config folder is a file, or read-only): the old login
// is still used, nothing is copied, and Save still saves.
func TestAttackLockUnavailable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	os.WriteFile(file, nil, 0o600)
	ro := filepath.Join(dir, "ro")
	os.Mkdir(ro, 0o500)
	t.Cleanup(func() { os.Chmod(ro, 0o700) })
	for name, lock := range map[string]string{"parent is a file": filepath.Join(file, ".login.lock"), "read-only folder": filepath.Join(ro, "sub", ".login.lock")} {
		t.Run(name, func(t *testing.T) {
			if os.Geteuid() == 0 && name == "read-only folder" {
				t.Skip("root")
			}
			s, cur, _ := legacyWith(t, lock)
			c, err := s.Load(ctx)
			if err != nil || (c.RefreshToken != "OLD" && c.RefreshToken != "NEW") {
				t.Fatalf("Load = %+v %v", c, err)
			}
			if cur.saves != 0 {
				t.Errorf("copied without the lock: %d saves", cur.saves)
			}
			if err := s.Save(ctx, &Credentials{RefreshToken: "NEW"}); err != nil || cur.c.RefreshToken != "NEW" {
				t.Fatalf("Save: %v %+v", err, cur.c)
			}
		})
	}
}

// ctx cancelled while another holder has the lock: Load returns quickly with the old
// login, Save still saves.
func TestAttackCtxCancelledWhileWaiting(t *testing.T) {
	lock := filepath.Join(t.TempDir(), ".login.lock")
	hold, err := lockCopy(context.Background(), lock)
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	s, cur, _ := legacyWith(t, lock)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	c, err := s.Load(ctx)
	if err != nil || c.RefreshToken != "OLD" || cur.saves != 0 {
		t.Fatalf("Load = %+v %v saves=%d", c, err, cur.saves)
	}
	if time.Since(start) > 3*time.Second {
		t.Errorf("waited %v", time.Since(start))
	}
	if err := s.Save(ctx, &Credentials{RefreshToken: "NEW"}); err != nil || cur.c.RefreshToken != "NEW" {
		t.Fatalf("Save: %v", err)
	}
}

// The lock is released after Load and Save (a second call does not wait 30s).
func TestAttackLockReleased(t *testing.T) {
	ctx := context.Background()
	s, _, _ := legacyWith(t, filepath.Join(t.TempDir(), ".login.lock"))
	start := time.Now()
	s.Load(ctx)
	s.Save(ctx, &Credentials{RefreshToken: "x"})
	s.Load(ctx)
	un, err := lockCopy(ctx, s.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	un()
	if time.Since(start) > 2*time.Second {
		t.Errorf("slow: %v", time.Since(start))
	}
}
