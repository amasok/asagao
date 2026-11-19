package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/termsafe"
)

// #68 attack: build a genuine user_version=24 SQLite database (no tags.workspace_gid column
// at all), populate tags attached via tasks.workspace_gid only, via task_memberships ->
// projects.workspace_gid only (tasks.workspace_gid=''), via a mix of empty-ws and
// project-ws tasks, across multiple tasks, and unused tags; then run migration 25 via Open.
func TestMigrate25FromRealV24SchemaAttack(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v24.db")

	idx := -1
	for i, m := range migrations {
		if strings.Contains(m, "ALTER TABLE tags ADD COLUMN workspace_gid") {
			idx = i
		}
	}
	if idx != 24 {
		t.Fatalf("expected tags.workspace_gid migration at index 24 (25th), got %d", idx)
	}

	// Build a genuine v24 database directly without ever running migration 25.
	rawDB, err := sql.Open("sqlite", path)
	must(t, err)
	for i := 0; i < idx; i++ {
		if _, err := rawDB.ExecContext(ctx, migrations[i]); err != nil {
			rawDB.Close()
			t.Fatalf("migration %d: %v", i, err)
		}
	}
	if _, err := rawDB.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", idx)); err != nil {
		rawDB.Close()
		t.Fatal(err)
	}

	// Verify tags.workspace_gid does NOT exist in v24.
	var hasCol int
	must(t, rawDB.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('tags') WHERE name = 'workspace_gid'").Scan(&hasCol))
	if hasCol != 0 {
		rawDB.Close()
		t.Fatalf("v24 schema unexpectedly already has tags.workspace_gid")
	}

	// Seed workspaces, projects, tasks, memberships, tags, task_tags directly on v24 schema.
	seed := []string{
		`INSERT INTO workspaces(gid, name) VALUES ('w1', 'W1'), ('w2', 'W2'), ('w3', 'W3')`,
		`INSERT INTO projects(gid, name, workspace_gid) VALUES ('p2', 'P2', 'w2'), ('p3', 'P3', 'w3')`,
		// t_direct: tasks.workspace_gid only, no memberships
		`INSERT INTO tasks(gid, name, workspace_gid) VALUES ('t_direct', 'Direct', 'w1')`,
		// t_proj: tasks.workspace_gid='', membership -> projects.workspace_gid='w2'
		`INSERT INTO tasks(gid, name, workspace_gid) VALUES ('t_proj', 'ViaProj', '')`,
		`INSERT INTO task_memberships(task_gid, project_gid) VALUES ('t_proj', 'p2')`,
		// t_empty: no workspace and no membership; t_proj3: project in w3
		`INSERT INTO tasks(gid, name, workspace_gid) VALUES ('t_empty', 'Empty', ''), ('t_proj3', 'ViaProj3', '')`,
		`INSERT INTO task_memberships(task_gid, project_gid) VALUES ('t_proj3', 'p3')`,
		// t_a (w1) and t_b (w2) both carry g_multi -> deterministic ORDER BY tt.task_gid -> w1
		`INSERT INTO tasks(gid, name, workspace_gid) VALUES ('t_a', 'A', 'w1'), ('t_b', 'B', 'w2')`,
		// Tags on v24 schema (no workspace_gid column)
		`INSERT INTO tags(gid, name, color) VALUES
			('g_direct', '直接タグ', 'red'),
			('g_via_proj', 'プロジェクト経由タグ', 'blue'),
			('g_skip_empty', '空スキップタグ', 'green'),
			('g_multi', '複数タスクタグ', ''),
			('g_unused', '未使用タグ', 'orange')`,
		`INSERT INTO task_tags(task_gid, tag_gid) VALUES
			('t_direct', 'g_direct'),
			('t_proj', 'g_via_proj'),
			('t_empty', 'g_skip_empty'),
			('t_proj3', 'g_skip_empty'),
			('t_a', 'g_multi'),
			('t_b', 'g_multi')`,
	}
	for _, q := range seed {
		if _, err := rawDB.ExecContext(ctx, q); err != nil {
			rawDB.Close()
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	must(t, rawDB.Close())

	// Open with current code -> runs migration 25.
	st, err := Open(ctx, path)
	must(t, err)
	defer st.Close()

	want := map[string]string{
		"g_direct":     "w1",
		"g_via_proj":   "w2",
		"g_skip_empty": "w3",
		"g_multi":      "w1",
		"g_unused":     "",
	}
	for gid, wantWS := range want {
		var col string
		must(t, st.db.QueryRowContext(ctx, "SELECT workspace_gid FROM tags WHERE gid = ?", gid).Scan(&col))
		if col != wantWS {
			t.Errorf("after migration 25: column tags.workspace_gid[%s] = %q, want %q", gid, col, wantWS)
		}
		if got, err := st.TagWorkspace(ctx, gid); err != nil || got != wantWS {
			t.Errorf("after migration 25: TagWorkspace(%s) = %q, %v; want %q", gid, got, err, wantWS)
		}
	}

	// Delete every task (cascading task_tags and task_memberships): backfilled tags keep their workspace.
	_, err = st.db.ExecContext(ctx, "DELETE FROM tasks")
	must(t, err)
	for gid, wantWS := range want {
		if got, err := st.TagWorkspace(ctx, gid); err != nil || got != wantWS {
			t.Errorf("after deleting tasks: TagWorkspace(%s) = %q, %v; want %q", gid, got, err, wantWS)
		}
	}

	// Re-opening is idempotent.
	must(t, st.Close())
	st, err = Open(ctx, path)
	must(t, err)
	defer st.Close()
	for gid, wantWS := range want {
		if got, err := st.TagWorkspace(ctx, gid); err != nil || got != wantWS {
			t.Errorf("after reopen: TagWorkspace(%s) = %q, %v; want %q", gid, got, err, wantWS)
		}
	}
}

// #68 attack: an existing non-empty tags.workspace_gid must never be wiped by empty
// workspace inputs through any write path (UpsertTags, AddTags, UpsertTasks, ApplyTag,
// AddTagLocal, ReplaceLocalTagGID).
func TestTagWorkspaceEmptyInputNeverWipesAttack(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	defer st.Close()

	must(t, st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "W1"}, {GID: "w2", Name: "W2"}}))
	must(t, st.UpsertProjects(ctx, "w2", []asana.Project{{GID: "p2", Name: "P2"}}, "me"))
	must(t, st.UpsertTags(ctx, []asana.Tag{
		{GID: "g1", Name: "急ぎ", Color: "red", Workspace: &asana.Ref{GID: "w1"}},
	}))

	assertWS := func(step, gid, want string) {
		t.Helper()
		var col string
		must(t, st.db.QueryRowContext(ctx, "SELECT workspace_gid FROM tags WHERE gid = ?", gid).Scan(&col))
		if col != want {
			t.Fatalf("%s: tags.workspace_gid[%s] = %q, want %q", step, gid, col, want)
		}
		if got, err := st.TagWorkspace(ctx, gid); err != nil || got != want {
			t.Fatalf("%s: TagWorkspace(%s) = %q, %v; want %q", step, gid, got, err, want)
		}
	}

	// 1. UpsertTags with nil Workspace and empty GID
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎA", Color: "blue", Workspace: nil}}))
	assertWS("UpsertTags(nil)", "g1", "w1")
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎB", Color: "green", Workspace: &asana.Ref{GID: ""}}}))
	assertWS("UpsertTags(empty)", "g1", "w1")

	// 2. AddTags with nil, empty, and even a different workspace (AddTags only fills '')
	n, err := st.AddTags(ctx, []asana.Tag{
		{GID: "g1", Name: "急ぎC", Workspace: nil},
		{GID: "g1", Name: "急ぎD", Workspace: &asana.Ref{GID: ""}},
		{GID: "g1", Name: "急ぎE", Workspace: &asana.Ref{GID: "w2"}},
	})
	if err != nil || n != 0 {
		t.Fatalf("AddTags existing = %d, %v; want 0, nil", n, err)
	}
	assertWS("AddTags(nil/empty/other)", "g1", "w1")

	// 3. UpsertTasks with no workspace and no known project
	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "t_empty", Name: "無所属", Tags: []asana.Ref{{GID: "g1", Name: "急ぎF"}}},
		{GID: "t_unknown_proj", Name: "未知プロ",
			Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p_missing"}}},
			Tags:        []asana.Ref{{GID: "g1", Name: "急ぎG"}}},
	}))
	assertWS("UpsertTasks(empty ws)", "g1", "w1")

	// 4. ApplyTag with nil and empty Workspace
	must(t, st.ApplyTag(ctx, asana.Tag{GID: "g1", Name: "急ぎH", Color: "yellow", Workspace: nil}))
	assertWS("ApplyTag(nil)", "g1", "w1")
	must(t, st.ApplyTag(ctx, asana.Tag{GID: "g1", Name: "急ぎI", Color: "orange", Workspace: &asana.Ref{GID: ""}}))
	assertWS("ApplyTag(empty)", "g1", "w1")

	// 5. AddTagLocal on a task with empty workspace (and on a task in w2) keeps w1
	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "t_w2", Name: "W2タスク", Workspace: &asana.Ref{GID: "w2"}},
	}))
	if _, err := st.AddTagLocal(ctx, "t_empty", "g1", "", testNow); err != nil {
		t.Fatal(err)
	}
	assertWS("AddTagLocal(empty task)", "g1", "w1")
	if _, err := st.AddTagLocal(ctx, "t_w2", "g1", "", testNow); err != nil {
		t.Fatal(err)
	}
	assertWS("AddTagLocal(other ws task)", "g1", "w1")

	// 6. ReplaceLocalTagGID: local has '' while real has 'w1' -> preserves 'w1'
	_, err = st.db.ExecContext(ctx, "INSERT INTO tags(gid, name, workspace_gid) VALUES('local:t1', 'ローカル', '')")
	must(t, err)
	must(t, st.ReplaceLocalTagGID(ctx, "local:t1", "g1"))
	assertWS("ReplaceLocalTagGID(local='', real='w1')", "g1", "w1")

	// 7. ReplaceLocalTagGID: local has 'w2' while real does not exist -> real gets 'w2'
	_, err = st.db.ExecContext(ctx, "INSERT INTO tags(gid, name, workspace_gid) VALUES('local:t2', 'ローカル2', 'w2')")
	must(t, err)
	must(t, st.ReplaceLocalTagGID(ctx, "local:t2", "g_new_real"))
	assertWS("ReplaceLocalTagGID(local='w2', real=new)", "g_new_real", "w2")

	// 8. UpsertTasks with project-only workspace backfills an empty tags.workspace_gid
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g_empty_ws", Name: "空WS"}}))
	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "t_p2", Name: "P2タスク",
			Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p2"}}},
			Tags:        []asana.Ref{{GID: "g_empty_ws", Name: "空WS"}}},
	}))
	assertWS("UpsertTasks(project ws backfill)", "g_empty_ws", "w2")
}

// #68 attack: unused tags across multiple workspaces preserve workspace_gid across
// UpdateTagLocal, DeleteTagLocal, UpsertTags while pending delete, and DiscardOp (undoTagOp).
func TestUnusedTagOpsAndUndoPreserveWorkspaceAttack(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	defer st.Close()

	must(t, st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w0", Name: "Default"}, {GID: "w1", Name: "Free"}, {GID: "w2", Name: "Paid"}}))
	must(t, st.UpsertTags(ctx, []asana.Tag{
		{GID: "g_w1", Name: "無料タグ", Color: "dark-red", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "g_w2", Name: "有料タグ", Color: "dark-blue", Workspace: &asana.Ref{GID: "w2"}},
	}))

	// Rename & SetColor on unused tag preserve workspace_gid; discarding preserves it too.
	must(t, st.UpdateTagLocal(ctx, "g_w1", map[string]any{"name": "改名後", "color": "dark-green"}, testNow))
	if ws, err := st.TagWorkspace(ctx, "g_w1"); err != nil || ws != "w1" {
		t.Fatalf("after UpdateTagLocal: ws = %q, %v; want w1", ws, err)
	}
	ops, err := st.ListOps(ctx)
	must(t, err)
	if len(ops) != 1 || ops[0].Kind != OpUpdateTag {
		t.Fatalf("ops = %+v", ops)
	}
	must(t, st.DiscardOp(ctx, ops[0].ID))
	if ws, err := st.TagWorkspace(ctx, "g_w1"); err != nil || ws != "w1" {
		t.Fatalf("after DiscardOp(update_tag): ws = %q, %v; want w1", ws, err)
	}

	// Delete unused tag -> outbox payload carries workspace="w1".
	must(t, st.DeleteTagLocal(ctx, "g_w1", testNow))
	// A sync poll while delete is still pending must keep it deleted via reapplyTagEdits.
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g_w1", Name: "無料タグ", Color: "dark-red", Workspace: &asana.Ref{GID: "w1"}}}))
	if _, err := st.GetTag(ctx, "g_w1"); err == nil {
		t.Fatal("deleted tag reappeared while delete_tag was pending")
	}

	ops, err = st.ListOps(ctx)
	must(t, err)
	if len(ops) != 1 || ops[0].Kind != OpDeleteTag || ops[0].Payload["workspace"] != "w1" {
		t.Fatalf("delete_tag op = %+v, want workspace w1", ops)
	}

	// Mark failed (as after 402) and discard -> undoTagOp restores name, color, AND workspace_gid.
	must(t, st.MarkOpFailed(ctx, ops[0].ID, fmt.Errorf("402 payment required")))
	must(t, st.DiscardOp(ctx, ops[0].ID))

	tc, err := st.GetTag(ctx, "g_w1")
	must(t, err)
	if tc.Name != "無料タグ" || tc.Color != "dark-red" {
		t.Fatalf("restored tag = %+v, want name=無料タグ color=dark-red", tc)
	}
	var col string
	must(t, st.db.QueryRowContext(ctx, "SELECT workspace_gid FROM tags WHERE gid = 'g_w1'").Scan(&col))
	if col != "w1" {
		t.Fatalf("restored tags.workspace_gid = %q, want w1", col)
	}
	if ws, err := st.TagWorkspace(ctx, "g_w1"); err != nil || ws != "w1" {
		t.Fatalf("restored TagWorkspace = %q, %v; want w1", ws, err)
	}
	// g_w2 untouched.
	if ws, err := st.TagWorkspace(ctx, "g_w2"); err != nil || ws != "w2" {
		t.Fatalf("g_w2 TagWorkspace = %q, %v; want w2", ws, err)
	}
}

// #68 attack: control characters (CSI, OSC, C0, C1, bidi) in tag names and workspace_gid
// must be stripped by termsafe on every read path including TagWorkspace, DeleteTagLocal,
// ListOps, and DiscardOp (undoTagOp).
func TestTagWorkspaceAndNameTermsafeAttack(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	defer st.Close()

	evil := "\x1b[31m\x1b]0;pwn\x07\x00\x07\r\n\u009b31m\u202e"
	rawName := "危険" + evil + "タグ"
	rawWS := "w1" + evil + "x"
	wantName := termsafe.String(rawName)
	wantWS := termsafe.String(rawWS)
	if !termsafe.Safe(wantName) || !termsafe.Safe(wantWS) || strings.ContainsAny(wantName, "\x1b\x00\x07\r") || strings.ContainsAny(wantWS, "\x1b\x00\x07\r") {
		t.Fatalf("sanity: termsafe left control chars: name=%q ws=%q", wantName, wantWS)
	}

	must(t, st.UpsertTags(ctx, []asana.Tag{
		{GID: "g_evil", Name: rawName, Color: "red", Workspace: &asana.Ref{GID: rawWS}},
	}))

	// Direct read paths go through termsafe_driver.
	if ws, err := st.TagWorkspace(ctx, "g_evil"); err != nil || ws != wantWS {
		t.Fatalf("TagWorkspace = %q (%v), want sanitized %q", ws, err, wantWS)
	}
	tc, err := st.GetTag(ctx, "g_evil")
	must(t, err)
	if tc.Name != wantName {
		t.Fatalf("GetTag.Name = %q, want %q", tc.Name, wantName)
	}
	list, err := st.ListTags(ctx)
	must(t, err)
	if len(list) != 1 || list[0].Name != wantName {
		t.Fatalf("ListTags = %+v, want name %q", list, wantName)
	}

	// AddTags path with control characters.
	n, err := st.AddTags(ctx, []asana.Tag{
		{GID: "g_evil2", Name: rawName, Workspace: &asana.Ref{GID: rawWS}},
	})
	if err != nil || n != 1 {
		t.Fatalf("AddTags = %d, %v", n, err)
	}
	if ws, err := st.TagWorkspace(ctx, "g_evil2"); err != nil || ws != wantWS {
		t.Fatalf("AddTags TagWorkspace = %q (%v), want %q", ws, err, wantWS)
	}

	// DeleteTagLocal -> outbox payload -> DiscardOp (undoTagOp) -> TagWorkspace & GetTag.
	must(t, st.DeleteTagLocal(ctx, "g_evil", testNow))
	ops, err := st.ListOps(ctx)
	must(t, err)
	if len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if got, _ := ops[0].Payload["workspace"].(string); got != wantWS {
		t.Fatalf("delete_tag payload workspace = %q, want %q", got, wantWS)
	}
	if got, _ := ops[0].Payload["name"].(string); got != wantName {
		t.Fatalf("delete_tag payload name = %q, want %q", got, wantName)
	}
	must(t, st.DiscardOp(ctx, ops[0].ID))
	if ws, err := st.TagWorkspace(ctx, "g_evil"); err != nil || ws != wantWS {
		t.Fatalf("after undoTagOp TagWorkspace = %q (%v), want %q", ws, err, wantWS)
	}

	// Even if a malicious JSON \u001b escape was stored directly in outbox.payload,
	// DiscardOp (termsafe.Unmarshal) must sanitize before restoring into tags.
	_, err = st.db.ExecContext(ctx, `INSERT INTO outbox(kind, task_gid, payload, base, state, created_at)
		VALUES(?, 'g_json_evil', '{"name":"N\u001b[31m","color":"","workspace":"w9\u001b[32m","tasks":[]}', '{}', 'failed', ?)`,
		OpDeleteTag, formatTime(testNow))
	must(t, err)
	ops, err = st.ListOps(ctx)
	must(t, err)
	var id int64
	for _, o := range ops {
		if o.TaskGID == "g_json_evil" {
			id = o.ID
		}
	}
	if id == 0 {
		t.Fatal("injected op not found")
	}
	must(t, st.DiscardOp(ctx, id))
	if ws, err := st.TagWorkspace(ctx, "g_json_evil"); err != nil || ws != termsafe.String("w9\x1b[32m") {
		t.Fatalf("after JSON-escaped undoTagOp: TagWorkspace = %q, %v", ws, err)
	}
	if tc, err := st.GetTag(ctx, "g_json_evil"); err != nil || tc.Name != termsafe.String("N\x1b[31m") {
		t.Fatalf("after JSON-escaped undoTagOp: GetTag = %+v, %v", tc, err)
	}
}

// #68 attack: concurrent UpsertTags (with empty workspace), AddTags, ApplyTag, and
// TagWorkspace reads are race-free and never wipe a known workspace_gid.
func TestTagWorkspaceConcurrentReadsWritesAttack(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	defer st.Close()

	must(t, st.UpsertTags(ctx, []asana.Tag{
		{GID: "g1", Name: "並行タグ", Color: "red", Workspace: &asana.Ref{GID: "w1"}},
	}))

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			_ = st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "並行タグ", Color: "red"}})
		}()
		go func() {
			defer wg.Done()
			_, _ = st.AddTags(ctx, []asana.Tag{{GID: "g1", Name: "並行タグ", Workspace: &asana.Ref{GID: "w2"}}})
		}()
		go func() {
			defer wg.Done()
			_ = st.ApplyTag(ctx, asana.Tag{GID: "g1", Name: "並行タグ", Color: "blue"})
		}()
		go func() {
			defer wg.Done()
			ws, err := st.TagWorkspace(ctx, "g1")
			if err != nil {
				t.Errorf("TagWorkspace err: %v", err)
			}
			if ws != "w1" {
				t.Errorf("concurrent TagWorkspace = %q, want w1", ws)
			}
		}()
	}
	wg.Wait()
	if ws, err := st.TagWorkspace(ctx, "g1"); err != nil || ws != "w1" {
		t.Fatalf("final TagWorkspace = %q, %v; want w1", ws, err)
	}
}
