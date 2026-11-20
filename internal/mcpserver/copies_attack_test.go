package mcpserver

import (
	"context"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/wire"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file the policy refuses is not copied into the cache and queues nothing; one it
// accepts is copied under the server's CacheDir.
func TestUploadAttachmentPolicyBeforeWriteAndCacheDir(t *testing.T) {
	st := seed(t)
	allowed, other, cache := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(dir string) string {
		p := filepath.Join(dir, "a.pdf")
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cs := connect(t, &Server{App: wire.UseCases(st), CacheDir: cache, Upload: UploadPolicy{Allowed: []string{allowed}}})
	if res := call(t, cs, "upload_attachment", map[string]any{"task_gid": "t1", "path": write(other)}, nil); !res.IsError {
		t.Error("a file outside the allowed folders was accepted")
	}
	if got := opList(t, st); len(got) != 0 {
		t.Errorf("queued %v", got)
	}
	if es, _ := os.ReadDir(cache); len(es) != 0 {
		t.Errorf("cache has %d entries after refusals", len(es))
	}
	if res := call(t, cs, "upload_attachment", map[string]any{"task_gid": "no-such", "path": write(allowed)}, nil); !res.IsError {
		t.Error("an unknown task was accepted")
	}
	if res := call(t, cs, "upload_attachment", map[string]any{"task_gid": "t1", "path": write(allowed)}, nil); res.IsError {
		t.Fatal(toolError(res))
	}
	d, _ := st.GetTask(context.Background(), "t1")
	if len(d.Attachments) != 1 || !strings.HasPrefix(d.Attachments[0].LocalPath, cache) {
		t.Errorf("attachments = %+v, want one under %s", d.Attachments, cache)
	}
}

// Duplicating a task that does not exist queues nothing.
func TestDuplicateUnknownTaskQueuesNothing(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{App: wire.UseCases(st)})
	if res := call(t, cs, "duplicate_task", map[string]any{"gid": "no-such"}, nil); !res.IsError {
		t.Error("should fail")
	}
	if got := opList(t, st); len(got) != 0 {
		t.Errorf("queued %v", got)
	}
}

// The write tools stamp their change with the server's clock, not the wall clock.
func TestWritesUseTheServerClock(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{App: wire.UseCases(st)})
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"move_task", map[string]any{"gid": "t1", "section": "手続き", "project": "新居"}},
		{"rename_section", map[string]any{"project": "新居", "section": "家具", "name": "大型家具"}},
		{"duplicate_task", map[string]any{"gid": "t1"}},
	} {
		if res := call(t, cs, tc.tool, tc.args, nil); res.IsError {
			t.Fatalf("%s: %s", tc.tool, toolError(res))
		}
	}
	ops, _ := st.ListOps(context.Background())
	if len(ops) != 3 {
		t.Fatalf("ops = %d", len(ops))
	}
	for _, op := range ops {
		if !strings.HasPrefix(op.CreatedAt, testNow.UTC().Format("2006-01-02")) && !strings.HasPrefix(op.CreatedAt, testNow.Format("2006-01-02")) {
			t.Errorf("%s created_at = %s, want the server's clock (%s)", op.Kind, op.CreatedAt, testNow)
		}
	}
}

// One notification that does not resolve, after ones that do, marks none of them.
func TestMarkNotificationsBadIDMarksNothing(t *testing.T) {
	st := seedInbox(t, store.InboxState{Status: store.InboxOK})
	cs := connect(t, &Server{App: wire.UseCases(st)})
	if res := call(t, cs, "mark_notifications", map[string]any{"gids": []string{"5550001", "nope"}}, nil); !res.IsError {
		t.Fatal("should fail")
	}
	if got := opList(t, st); len(got) != 0 {
		t.Errorf("queued %v", got)
	}
	if n, _ := st.UnreadNotifications(context.Background()); n != 2 {
		t.Errorf("unread = %d, want 2", n)
	}
}
