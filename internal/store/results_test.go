package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

func resultsStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "WS"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "棚", Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	return st
}

func mustSet(t *testing.T, st *Store, kv ...string) {
	t.Helper()
	for i := 0; i < len(kv); i += 2 {
		if err := st.SetSyncState(context.Background(), kv[i], kv[i+1]); err != nil {
			t.Fatal(err)
		}
	}
}

func syncStateOf(t *testing.T, st *Store, key string) string {
	t.Helper()
	v, err := st.SyncState(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func prune(t *testing.T, st *Store, now time.Time) int {
	t.Helper()
	n, err := st.PruneResults(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// #44: a mapping nothing needs is deleted ResultGrace after a prune first found it so,
// with its stamp; until then lookups by the local gid keep working.
func TestPruneResultsAfterGrace(t *testing.T) {
	ctx := context.Background()
	st := resultsStore(t)
	keys := []string{resultPrefixTask + "local:a", resultPrefixStory + "local:b", resultPrefixAttachment + "local:c", resultPrefixStatus + "local:d", resultPrefixTag + "local:e"}
	for _, k := range keys {
		mustSet(t, st, k, "1200")
	}
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	if n := prune(t, st, now); n != 0 {
		t.Fatalf("first prune deleted %d", n)
	}
	for _, k := range keys {
		if syncStateOf(t, st, k) != "1200" || syncStateOf(t, st, keyResultSeen+k) != now.Format(time.RFC3339) {
			t.Fatalf("%s: should be kept and stamped", k)
		}
	}
	if g, _ := st.RealGID(ctx, "local:a"); g != "1200" {
		t.Fatalf("RealGID during the grace = %q", g)
	}
	if n := prune(t, st, now.Add(ResultGrace-time.Minute)); n != 0 {
		t.Fatalf("deleted %d before the grace was over", n)
	}
	if n := prune(t, st, now.Add(ResultGrace)); n != len(keys) {
		t.Fatalf("deleted %d, want %d", n, len(keys))
	}
	for _, k := range keys {
		if syncStateOf(t, st, k) != "" || syncStateOf(t, st, keyResultSeen+k) != "" {
			t.Fatalf("%s: should be gone with its stamp", k)
		}
	}
	if g, _ := st.RealGID(ctx, "local:a"); g != "local:a" {
		t.Fatalf("RealGID after = %q", g)
	}
}

// A mapping is kept, and its grace starts over, while an outbox entry refers to the local
// gid (as its task, or in its payload) or a row still has it.
func TestPruneResultsKeepsReferenced(t *testing.T) {
	ctx := context.Background()
	st := resultsStore(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	mustSet(t, st, resultPrefixTask+"local:a", "1200", resultPrefixTask+"local:p", "1300", resultPrefixTag+"local:tag", "1400")
	prune(t, st, now) // all stamped

	// t1 now waits for local:a (an entry queued before the task was sent, say)
	if _, err := st.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, created_at) VALUES (?, 't1', '{"dependency":"local:a","dependency_name":"棚板"}', ?)`, OpAddDependency, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// an outbox entry whose task is local:p
	if _, err := st.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, created_at) VALUES (?, 'local:p', '{"completed":true}', ?)`, OpUpdateTask, now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	// a tag row that still has the local gid
	if err := st.UpsertTags(ctx, []asana.Tag{{GID: "local:tag", Name: "急ぎ"}}); err != nil {
		t.Fatal(err)
	}
	later := now.Add(2 * ResultGrace)
	if n := prune(t, st, later); n != 0 {
		t.Fatalf("deleted %d referenced mappings", n)
	}
	for _, k := range []string{resultPrefixTask + "local:a", resultPrefixTask + "local:p", resultPrefixTag + "local:tag"} {
		if syncStateOf(t, st, k) == "" {
			t.Errorf("%s deleted", k)
		}
		if syncStateOf(t, st, keyResultSeen+k) != "" {
			t.Errorf("%s: the grace should start over while it is needed", k)
		}
	}

	// once they are gone, the grace starts then
	if _, err := st.db.ExecContext(ctx, "DELETE FROM outbox"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, "DELETE FROM tags"); err != nil {
		t.Fatal(err)
	}
	if n := prune(t, st, later); n != 0 {
		t.Fatalf("deleted %d without a grace", n)
	}
	if n := prune(t, st, later.Add(ResultGrace)); n != 3 {
		t.Fatalf("deleted %d, want 3", n)
	}
}

// While a comment (or a task, attachment, status update) is queued, mappings of that
// kind are kept: verifying an uncertain send relies on them to not take an item that is
// already another entry's. Other kinds are not held back.
func TestPruneResultsKeepsWhileSameKindQueued(t *testing.T) {
	ctx := context.Background()
	st := resultsStore(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	mustSet(t, st, resultPrefixStory+"local:old", "1500", resultPrefixTask+"local:old", "1600")
	prune(t, st, now)
	if _, err := st.AddRichCommentLocal(ctx, "t1", "次", "", UserRef{GID: "me", Name: "Me"}, now); err != nil {
		t.Fatal(err)
	}
	if n := prune(t, st, now.Add(ResultGrace)); n != 1 {
		t.Fatalf("deleted %d, want only the task mapping", n)
	}
	if syncStateOf(t, st, resultPrefixStory+"local:old") == "" {
		t.Fatal("a story mapping was deleted while a comment is queued")
	}
	if syncStateOf(t, st, resultPrefixTask+"local:old") != "" {
		t.Fatal("the task mapping should not be held back by a comment")
	}
}

// Only mappings of local gids are touched; stamps whose mapping is gone are removed, and a
// stamp in the future (clock changed) is not trusted.
func TestPruneResultsLeavesOtherKeys(t *testing.T) {
	st := resultsStore(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	others := []string{resultPrefixTask + "1234", keyMeGID, keyLastSync, keyCustomFields + "p1", resultPrefixTag + "5678"}
	for _, k := range others {
		mustSet(t, st, k, "x")
	}
	mustSet(t, st, keyResultSeen+resultPrefixTask+"local:gone", now.Format(time.RFC3339),
		resultPrefixStory+"local:f", "1700", keyResultSeen+resultPrefixStory+"local:f", now.Add(3*ResultGrace).Format(time.RFC3339))
	if n := prune(t, st, now.Add(2*ResultGrace)); n != 0 {
		t.Fatalf("deleted %d", n)
	}
	for _, k := range others {
		if syncStateOf(t, st, k) != "x" {
			t.Errorf("%s should not be touched", k)
		}
	}
	if syncStateOf(t, st, keyResultSeen+resultPrefixTask+"local:gone") != "" {
		t.Error("a stamp without its mapping should be removed")
	}
	if got := syncStateOf(t, st, keyResultSeen+resultPrefixStory+"local:f"); got != now.Add(2*ResultGrace).Format(time.RFC3339) {
		t.Errorf("a future stamp should start over now: %q", got)
	}
	if syncStateOf(t, st, resultPrefixStory+"local:f") == "" {
		t.Error("deleted on an untrusted stamp")
	}
}

// What sending an offline task records is pruned once its entry is done: end to end
// through CreateTaskLocal and CompleteCreate.
func TestPruneResultsAfterCreate(t *testing.T) {
	ctx := context.Background()
	st := resultsStore(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	local, err := st.CreateTaskLocal(ctx, NewTask{Name: "新しい", WorkspaceGID: "w1"}, now)
	if err != nil {
		t.Fatal(err)
	}
	ops, _ := st.ListOps(ctx)
	if len(ops) != 1 {
		t.Fatalf("ops %+v", ops)
	}
	prune(t, st, now)
	if syncStateOf(t, st, keyResultSeen+resultPrefixTask+local) != "" {
		t.Fatal("nothing to prune before it is sent")
	}
	if err := st.CompleteCreate(ctx, ops[0].ID, local, "1800", nil); err != nil {
		t.Fatal(err)
	}
	if g, _ := st.RealGID(ctx, local); g != "1800" {
		t.Fatalf("RealGID = %q", g)
	}
	prune(t, st, now)
	if n := prune(t, st, now.Add(ResultGrace)); n != 1 {
		t.Fatalf("deleted %d", n)
	}
	if g, _ := st.RealGID(ctx, local); g != local {
		t.Fatalf("RealGID after = %q", g)
	}
}

// Each kind of row that can still carry a local gid keeps its mapping (one case per query
// in resultTables).
func TestPruneResultsKeepsWhileRowHasLocalGID(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		insert    string // SQL putting the local gid "local:x" in a row
	}{
		{"task", resultPrefixTask + "local:x", `INSERT INTO tasks(gid, name) VALUES ('local:x', 'n')`},
		{"parent", resultPrefixTask + "local:x", `INSERT INTO tasks(gid, name, parent_gid) VALUES ('c1', 'n', 'local:x')`},
		{"waits for", resultPrefixTask + "local:x", `INSERT INTO task_dependencies(task_gid, dependency_gid) VALUES ('t1', 'local:x')`},
		{"waited on", resultPrefixTask + "local:x", `INSERT INTO task_dependencies(task_gid, dependency_gid) VALUES ('local:x', 't1')`},
		{"story", resultPrefixStory + "local:x", `INSERT INTO stories(gid, task_gid) VALUES ('local:x', 't1')`},
		{"attachment", resultPrefixAttachment + "local:x", `INSERT INTO attachments(gid, parent_gid) VALUES ('local:x', 't1')`},
		{"status update", resultPrefixStatus + "local:x", `INSERT INTO status_updates(gid, project_gid) VALUES ('local:x', 'p1')`},
		{"tag", resultPrefixTag + "local:x", `INSERT INTO tags(gid, name) VALUES ('local:x', 'n')`},
		{"tag on a task", resultPrefixTag + "local:x", `INSERT INTO task_tags(task_gid, tag_gid) VALUES ('t1', 'local:x')`},
		{"outbox task", resultPrefixStatus + "local:x", `INSERT INTO outbox(kind, task_gid, payload, created_at) VALUES ('update_task', 'local:x', '{}', '')`},
		{"outbox base", resultPrefixTag + "local:x", `INSERT INTO outbox(kind, task_gid, payload, base, created_at) VALUES ('remove_tag', 't1', '{}', '{"tag":"local:x"}', '')`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			st := resultsStore(t)
			now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
			mustSet(t, st, tc.key, "2000")
			if _, err := st.db.ExecContext(ctx, tc.insert); err != nil {
				t.Fatal(err)
			}
			prune(t, st, now)
			if n := prune(t, st, now.Add(2*ResultGrace)); n != 0 || syncStateOf(t, st, tc.key) != "2000" {
				t.Fatalf("deleted (%d) while a row has the local gid", n)
			}
		})
	}
}

// Each kind verified against the recorded results holds back the mappings of its prefix,
// and only those (one case per entry in resultKinds).
func TestPruneResultsKindsHoldTheirPrefix(t *testing.T) {
	prefixes := []string{resultPrefixTask, resultPrefixStory, resultPrefixAttachment, resultPrefixStatus, resultPrefixTag}
	for _, tc := range []struct{ kind, prefix string }{
		{OpCreateTask, resultPrefixTask},
		{OpDuplicateTask, resultPrefixTask},
		{OpAddComment, resultPrefixStory},
		{OpUploadAttachment, resultPrefixAttachment},
		{OpCreateStatus, resultPrefixStatus},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			ctx := context.Background()
			st := resultsStore(t)
			now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
			for _, p := range prefixes {
				mustSet(t, st, p+"local:old", "2100")
			}
			// queued for another task: it holds back the prefix, not a single mapping
			if _, err := st.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, created_at) VALUES (?, 't1', '{}', '')`, tc.kind); err != nil {
				t.Fatal(err)
			}
			prune(t, st, now)
			if n := prune(t, st, now.Add(ResultGrace)); n != len(prefixes)-1 {
				t.Fatalf("deleted %d, want all but %s", n, tc.prefix)
			}
			for _, p := range prefixes {
				if kept := syncStateOf(t, st, p+"local:old") != ""; kept != (p == tc.prefix) {
					t.Errorf("%s: kept = %v", p, kept)
				}
			}
		})
	}
}

// A local gid whose mapping was pruned is refused with a hint to use the Asana gid.
func TestResolveTaskGIDAfterPrune(t *testing.T) {
	st := resultsStore(t)
	_, err := st.ResolveTaskGID(context.Background(), "local:gone")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "Asana の gid") {
		t.Fatalf("err = %v", err)
	}
	if _, err := st.ResolveTaskGID(context.Background(), "999"); err == nil || strings.Contains(err.Error(), "Asana の gid") {
		t.Fatalf("a real gid needs no hint: %v", err)
	}
}

// The outbox is read once per prune; a local gid anywhere in a JSON string of the payload
// (also inside other text, or in a key) keeps its mapping, a shorter one does not.
func TestPruneResultsFindsLocalGIDsInPayloadText(t *testing.T) {
	ctx := context.Background()
	st := resultsStore(t)
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	mustSet(t, st, resultPrefixTask+"local:abc", "1", resultPrefixTask+"local:in", "2", resultPrefixTask+"local:key", "3",
		resultPrefixTask+"local:raw", "4", resultPrefixTask+"local:abcd", "5")
	for _, payload := range []string{
		`{"notes":"see local:abcdef and local:in, ok"}`, // longer gid: kept (conservative)
		`{"list":[{"local:key":true}]}`,
		`not json local:raw`,
		`{"x":"local:ab"}`, // shorter than local:abc: refers to neither
	} {
		if _, err := st.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, created_at) VALUES ('update_task', 't1', ?, '')`, payload); err != nil {
			t.Fatal(err)
		}
	}
	prune(t, st, now)
	prune(t, st, now.Add(ResultGrace))
	for k, kept := range map[string]bool{"local:abc": true, "local:abcd": true, "local:in": true, "local:key": true, "local:raw": true} {
		if got := syncStateOf(t, st, resultPrefixTask+k) != ""; got != kept {
			t.Errorf("%s kept = %v, want %v", k, got, kept)
		}
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM outbox WHERE payload LIKE '%abcdef%'`); err != nil {
		t.Fatal(err)
	}
	prune(t, st, now.Add(ResultGrace))
	prune(t, st, now.Add(3*ResultGrace))
	if syncStateOf(t, st, resultPrefixTask+"local:abc") != "" || syncStateOf(t, st, resultPrefixTask+"local:in") != "" {
		t.Error("no longer referred to: should go after the grace")
	}
	if syncStateOf(t, st, resultPrefixTask+"local:key") == "" {
		t.Error("still referred to")
	}
}
