package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/store"
)

// fileSums is a checksum of every file in dir (the store, its -wal and -shm).
func fileSums(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		fmt.Fprintf(&b, "%s %d %x\n", p, fi.Size(), sha256.Sum256(data))
		return nil
	})
	return b.String()
}

// Failing dry runs (an invalid command, a missing file, a directory to attach) leave no
// copy of the store in TMPDIR, no request to Asana, and the real store byte for byte.
func TestDryRunAttackFailuresLeaveNothing(t *testing.T) {
	path, requests, run := dryRunEnv(t)
	dir := filepath.Dir(path)
	before := fileSums(t, dir)
	adir := t.TempDir()
	for _, args := range [][]string{
		{"task", "complete", "nope"},
		{"task", "create", "x", "--due", "someday"},
		{"task", "attach", "t1", "/nonexistent/file"},
		{"task", "attach", "t1", adir},
		{"task", "attach", "t1", "/dev/null"},
		{"task", "move", "t1", "--section", "ない"},
		{"outbox", "retry", "999"},
		{"outbox", "resolve", "999", "--keep", "local"},
		{"project", "archive", "ない"},
		{"project", "status", "post", "ない", "--text", "x"},
		{"comment", "add", "nope", "x"},
		{"inbox", "read", "nope"},
	} {
		_, _, err := run(append(args, "--dry-run")...)
		if err == nil {
			t.Errorf("%v: no error", args)
		}
		if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
			t.Errorf("%v left %v in TMPDIR", args, left)
			_ = os.RemoveAll(filepath.Join(os.Getenv("TMPDIR"), left[0].Name()))
		}
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d requests", n)
	}
	if after := fileSums(t, dir); after != before {
		t.Errorf("the real store directory changed:\n%s\n--- after\n%s", before, after)
	}
}

// Successful dry runs: the real data directory is byte-identical (store, wal, anything
// else), the cache directory has nothing, and TMPDIR is empty again.
func TestDryRunAttackRealFilesUntouched(t *testing.T) {
	path, requests, run := dryRunEnv(t)
	dir := filepath.Dir(path)
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	file := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(file, make([]byte, 3<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	before := fileSums(t, dir)
	for _, args := range [][]string{
		{"task", "attach", "t1", file},
		{"task", "create", "a", "--project", "新居"},
		{"task", "complete", "t1", "t2"},
		{"comment", "add", "t1", "x"},
		{"project", "create", "p"},
		{"--json", "task", "duplicate", "t1"},
	} {
		if out, errOut, err := run(append(args, "--dry-run")...); err != nil {
			t.Fatalf("%v: %v\n%s%s", args, err, out, errOut)
		}
	}
	if after := fileSums(t, dir); after != before {
		t.Errorf("the real data directory changed")
	}
	if ents, _ := os.ReadDir(cache); len(ents) != 0 {
		t.Errorf("cache directory written: %v", ents)
	}
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Errorf("left in TMPDIR: %v", left)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d requests", n)
	}
}

// --dry-run=false is a real run (queued offline-style), and a dry run before it does not
// leave the flag set for the next command on the same process state.
func TestDryRunAttackFlagForms(t *testing.T) {
	path := writeCLIEnv(t)
	t.Setenv("TMPDIR", t.TempDir())
	if out, err := runCLI(t, "task", "complete", "t1", "--dry-run=false"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 1 {
		t.Fatalf("--dry-run=false did not queue: %v", opKinds(ops))
	}
	if out, err := runCLI(t, "task", "complete", "t2", "--dry-run", "--dry-run=false"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 2 {
		t.Errorf("last flag wins: %v", opKinds(ops))
	}
	// the flag does not exist where nothing is written
	if _, err := runCLI(t, "task", "list", "--dry-run"); err == nil {
		t.Error("task list accepted --dry-run")
	}
}

// The store being written by another process meanwhile: the dry run still works on a
// consistent copy, changes none of it, and the other writer's rows survive.
func TestDryRunAttackConcurrentWriter(t *testing.T) {
	path, _, run := dryRunEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	me, _ := st.Me(context.Background())
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = st.AddRichCommentLocal(context.Background(), "t2", fmt.Sprint("c", i), "<body>x</body>", me, time.Now())
		}
	}()
	for i := 0; i < 8; i++ {
		if out, errOut, err := run("task", "complete", "t1", "--dry-run"); err != nil {
			t.Errorf("run %d: %v\n%s%s", i, err, out, errOut)
		}
	}
	close(stop)
	wg.Wait()
	if left, _ := os.ReadDir(os.Getenv("TMPDIR")); len(left) != 0 {
		t.Errorf("left in TMPDIR: %v", left)
	}
	for _, op := range cliOps(t, path) {
		if op.Kind == store.OpUpdateTask && op.TaskGID == "t1" {
			t.Errorf("the dry run queued %v in the real outbox", op)
		}
	}
}

// Text from Asana (a task name, a project name) with terminal escapes is not printed raw
// by a dry run, in text or JSON-free output.
func TestDryRunAttackTerminalSafe(t *testing.T) {
	path, _, run := dryRunEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	out, _, err := run("comment", "add", "t1", "x\x1b]0;pwn\x07\u009b2J", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out, "\x1b\x07\u009b") {
		t.Errorf("control characters printed: %q", out)
	}
	out, _, _ = run("project", "create", "p\x1b[2J\u009b", "--dry-run")
	if strings.ContainsAny(out, "\x1b\x07\u009b") {
		t.Errorf("control characters printed: %q", out)
	}
}

// removeStaleDryRuns removes only old folders of dry runs: not a recent one (another dry
// run in progress), not a symlink (nor what it points to), not a plain file, and nothing
// that merely resembles the name.
func TestDryRunAttackStaleCleanup(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	now := time.Now()
	mk := func(name string, age time.Duration) string {
		p := filepath.Join(tmp, name)
		if err := os.MkdirAll(filepath.Join(p, "attachments"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "store.db"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-age)
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
		return p
	}
	stale := mk(dryRunPrefix+"old", 2*time.Hour)
	almost := mk(dryRunPrefix+"almost", 59*time.Minute)
	fresh := mk(dryRunPrefix+"fresh", 0)
	other := mk("something-else", 5*time.Hour)
	noDash := mk("asagao-dry-run", 5*time.Hour)
	target := mk("target-dir", 5*time.Hour)
	if err := os.Symlink(target, filepath.Join(tmp, dryRunPrefix+"link")); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(tmp, dryRunPrefix+"file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-5 * time.Hour)
	_ = os.Chtimes(file, old, old)

	removeStaleDryRuns(now)

	gone := func(p string) bool { _, err := os.Lstat(p); return os.IsNotExist(err) }
	if !gone(stale) {
		t.Error("the stale copy stayed")
	}
	for name, p := range map[string]string{"almost": almost, "fresh": fresh, "other": other, "no dash": noDash, "symlink target": target, "file": file, "link": filepath.Join(tmp, dryRunPrefix+"link")} {
		if gone(p) {
			t.Errorf("%s was removed", name)
		}
	}
}

// A dry run in progress keeps its copy when another dry run starts and cleans up.
func TestDryRunAttackSecondRunKeepsFirstCopy(t *testing.T) {
	_, _, run := dryRunEnv(t)
	tmp := os.Getenv("TMPDIR")
	live := filepath.Join(tmp, dryRunPrefix+"live")
	if err := os.MkdirAll(live, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, errOut, err := run("task", "complete", "t1", "--dry-run"); err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("another dry run's folder was removed: %v", err)
	}
}
