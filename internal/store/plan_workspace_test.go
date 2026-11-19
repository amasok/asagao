package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// #68: migrating a store from before tags.workspace_gid backfills existing tags from the
// tasks carrying them (directly or through their project), so TagWorkspace still knows
// their workspace even after the tasks are gone.
func TestMigrateTagsWorkspaceBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	idx := -1
	for i, m := range migrations {
		if strings.Contains(m, "ALTER TABLE tags ADD COLUMN workspace_gid") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no migration adds tags.workspace_gid")
	}
	st, err := Open(ctx, path)
	must(t, err)
	must(t, st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P1"}}, "me"))
	must(t, st.UpsertProjects(ctx, "w2", []asana.Project{{GID: "p2", Name: "P2"}}, "me"))
	must(t, st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"}, Tags: []asana.Ref{{GID: "g1", Name: "直接"}}},
		{GID: "t2", Name: "B", Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p2"}}}, Tags: []asana.Ref{{GID: "g2", Name: "プロジェクト経由"}}},
	}))
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g3", Name: "未使用"}}))
	rollbackTo(t, st.db, idx)
	st.Close()

	st, err = Open(ctx, path)
	must(t, err)
	defer st.Close()
	for gid, want := range map[string]string{"g1": "w1", "g2": "w2", "g3": ""} {
		var col string
		must(t, st.db.QueryRowContext(ctx, "SELECT workspace_gid FROM tags WHERE gid = ?", gid).Scan(&col))
		if col != want {
			t.Errorf("tags.workspace_gid[%s] = %q, want %q", gid, col, want)
		}
	}
	// Even after the tasks are deleted (and task_tags cascades), the tags keep their workspace.
	_, err = st.db.ExecContext(ctx, "DELETE FROM tasks")
	must(t, err)
	for gid, want := range map[string]string{"g1": "w1", "g2": "w2", "g3": ""} {
		if got, err := st.TagWorkspace(ctx, gid); err != nil || got != want {
			t.Errorf("TagWorkspace(%s) after tasks deleted = %q, %v; want %q", gid, got, err, want)
		}
	}
	// Opening again is a no-op.
	st.Close()
	st, err = Open(ctx, path)
	must(t, err)
	st.Close()
}

// #68: TagWorkspace reads tags.workspace_gid first (for unused tags too), falls back to
// task_tags -> tasks when empty, and local tag create/delete/undo preserve workspace_gid.
func TestTagWorkspaceDirectFallbackAndLocalOps(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	defer st.Close()

	must(t, st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w0", Name: "Default"}, {GID: "w1", Name: "Other"}}))
	// Unused tag in w1 (no task carries it).
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "未使用", Workspace: &asana.Ref{GID: "w1"}}}))
	if got, err := st.TagWorkspace(ctx, "g1"); err != nil || got != "w1" {
		t.Fatalf("unused tag workspace = %q, %v; want w1", got, err)
	}

	// Fallback: a tag whose tags.workspace_gid is empty still resolves via its task.
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t2", Name: "B", Workspace: &asana.Ref{GID: "w1"}}}))
	_, err = st.db.ExecContext(ctx, "INSERT INTO tags(gid, name, workspace_gid) VALUES('g2', '旧', '')")
	must(t, err)
	_, err = st.db.ExecContext(ctx, "INSERT INTO task_tags(task_gid, tag_gid) VALUES('t2', 'g2')")
	must(t, err)
	if got, err := st.TagWorkspace(ctx, "g2"); err != nil || got != "w1" {
		t.Fatalf("fallback tag workspace = %q, %v; want w1", got, err)
	}

	// AddTags (typeahead): stores workspace_gid for new tags, backfills empty ones without
	// counting them as newly added.
	n, err := st.AddTags(ctx, []asana.Tag{
		{GID: "g3", Name: "新規", Workspace: &asana.Ref{GID: "w1"}},
		{GID: "g2", Name: "旧", Workspace: &asana.Ref{GID: "w1"}},
	})
	if err != nil || n != 1 {
		t.Fatalf("AddTags = %d, %v; want 1, nil", n, err)
	}
	if got, err := st.TagWorkspace(ctx, "g3"); err != nil || got != "w1" {
		t.Fatalf("AddTags new tag workspace = %q, %v", got, err)
	}
	var g2col string
	must(t, st.db.QueryRowContext(ctx, "SELECT workspace_gid FROM tags WHERE gid = 'g2'").Scan(&g2col))
	if g2col != "w1" {
		t.Fatalf("AddTags did not backfill g2 workspace_gid = %q", g2col)
	}

	// AddTagLocal stores workspace_gid even if later removed from the task, and TagWorkspace
	// resolves a replaced local gid via RealTagGID.
	localGID, err := st.AddTagLocal(ctx, "t2", "", "ローカル", testNow)
	must(t, err)
	must(t, st.RemoveTagLocal(ctx, "t2", localGID, testNow))
	if got, err := st.TagWorkspace(ctx, localGID); err != nil || got != "w1" {
		t.Fatalf("local tag after untag: workspace = %q, %v; want w1", got, err)
	}
	must(t, st.ReplaceLocalTagGID(ctx, localGID, "g999"))
	if got, err := st.TagWorkspace(ctx, localGID); err != nil || got != "w1" {
		t.Fatalf("replaced local tag via old gid: workspace = %q, %v; want w1", got, err)
	}

	// DeleteTagLocal falls back to task_tags -> tasks when tags.workspace_gid is empty, so
	// pruning the task before push still keeps workspace in the outbox payload.
	_, err = st.db.ExecContext(ctx, "INSERT INTO tags(gid, name, workspace_gid) VALUES('g_empty_ws', '空WS', '')")
	must(t, err)
	_, err = st.db.ExecContext(ctx, "INSERT INTO task_tags(task_gid, tag_gid) VALUES('t2', 'g_empty_ws')")
	must(t, err)
	must(t, st.DeleteTagLocal(ctx, "g_empty_ws", testNow))
	_, err = st.db.ExecContext(ctx, "DELETE FROM tasks WHERE gid = 't2'")
	must(t, err)
	opsAfterDel, err := st.ListOps(ctx)
	must(t, err)
	var emptyDelOp *Op
	for i := range opsAfterDel {
		if opsAfterDel[i].Kind == OpDeleteTag && opsAfterDel[i].TaskGID == "g_empty_ws" {
			emptyDelOp = &opsAfterDel[i]
		}
	}
	if emptyDelOp == nil || emptyDelOp.Payload["workspace"] != "w1" {
		t.Fatalf("delete_tag fallback op = %+v, want workspace w1", emptyDelOp)
	}

	// DeleteTagLocal records workspace in the outbox payload and DiscardOp restores it.
	must(t, st.DeleteTagLocal(ctx, "g1", testNow))
	ops, err := st.ListOps(ctx)
	must(t, err)
	var delOp *Op
	for i := range ops {
		if ops[i].Kind == OpDeleteTag && ops[i].TaskGID == "g1" {
			delOp = &ops[i]
		}
	}
	if delOp == nil || delOp.Payload["workspace"] != "w1" {
		t.Fatalf("delete_tag op = %+v, want workspace w1", delOp)
	}
	must(t, st.DiscardOp(ctx, delOp.ID))
	if got, err := st.TagWorkspace(ctx, "g1"); err != nil || got != "w1" {
		t.Fatalf("after DiscardOp(delete_tag): workspace = %q, %v; want w1", got, err)
	}
}

// #68: an input without workspace_gid must never wipe a non-empty tags.workspace_gid.
func TestTagWorkspaceNotOverwrittenByEmpty(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	must(t, err)
	defer st.Close()

	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: "red", Workspace: &asana.Ref{GID: "w1"}}}))
	// UpsertTags with nil or empty Workspace keeps w1.
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: "blue"}}))
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ", Color: "blue", Workspace: &asana.Ref{GID: ""}}}))
	if got, err := st.TagWorkspace(ctx, "g1"); err != nil || got != "w1" {
		t.Fatalf("after UpsertTags without workspace: %q, %v", got, err)
	}
	// UpsertTasks with a task that has no workspace keeps w1.
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Tags: []asana.Ref{{GID: "g1", Name: "急ぎ"}}}}))
	if got, err := st.TagWorkspace(ctx, "g1"); err != nil || got != "w1" {
		t.Fatalf("after UpsertTasks without workspace: %q, %v", got, err)
	}
	// ApplyTag without workspace keeps w1.
	must(t, st.ApplyTag(ctx, asana.Tag{GID: "g1", Name: "至急", Color: "green"}))
	if got, err := st.TagWorkspace(ctx, "g1"); err != nil || got != "w1" {
		t.Fatalf("after ApplyTag without workspace: %q, %v", got, err)
	}
	// A non-empty workspace DOES update it.
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "至急", Workspace: &asana.Ref{GID: "w2"}}}))
	if got, err := st.TagWorkspace(ctx, "g1"); err != nil || got != "w2" {
		t.Fatalf("after UpsertTags with w2: %q, %v", got, err)
	}
}
