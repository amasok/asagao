package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// follow/project with no names changes nothing and queues nothing.
func TestRelationCommandsWithNoNamesQueueNothing(t *testing.T) {
	for _, cmd := range []string{"follow", "project", "tag"} {
		t.Run(cmd, func(t *testing.T) {
			path := relationsCLIEnv(t)
			out, err := runCLI(t, "task", cmd, "t1")
			t.Logf("err=%v out=%s", err, out)
			if ops := cliOps(t, path); len(ops) != 0 {
				t.Errorf("queued %v", opKinds(ops))
			}
		})
	}
}

// A move or rename that does not resolve fails and queues nothing.
func TestMoveAndRenameCommandsUnresolvedQueueNothing(t *testing.T) {
	for _, args := range [][]string{
		{"task", "move", "t1", "--section", "ない"},
		{"task", "move", "t1", "--section", "家具", "--project", "ない"},
		{"task", "move", "nope", "--section", "家具"},
		{"section", "rename", "ない", "x", "--project", "新居"},
		{"section", "rename", "家具", "x", "--project", "ない"},
		{"section", "rename", "家具", "", "--project", "新居"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := relationsCLIEnv(t)
			if out, err := runCLI(t, args...); err == nil {
				t.Errorf("should fail\n%s", out)
			}
			if ops := cliOps(t, path); len(ops) != 0 {
				t.Errorf("queued %v", opKinds(ops))
			}
		})
	}
}

// attach refuses a folder and a task that is not there, and queues nothing.
func TestAttachRefusalsQueueNothing(t *testing.T) {
	path := relationsCLIEnv(t)
	dir := t.TempDir()
	file := filepath.Join(dir, "a.pdf")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"task", "attach", "t1", dir},
		{"task", "attach", "no-such", file},
		{"task", "duplicate", "no-such"},
	} {
		if out, err := runCLI(t, args...); err == nil {
			t.Errorf("%v should fail\n%s", args, out)
		}
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("queued %v", opKinds(ops))
	}
}
