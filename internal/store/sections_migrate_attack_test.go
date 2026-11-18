package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// A store from before tag colors and My Tasks sections (the migration that adds
// my_task_sections) keeps its tasks, tags, outbox and sections, and gets empty new columns.
func TestMigrateStoreFromBeforeSectionsAndTagColors(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	idx := -1
	for i, m := range migrations {
		if strings.Contains(m, "CREATE TABLE my_task_sections") {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("no migration creates my_task_sections")
	}
	st, err := Open(ctx, path)
	must(t, err)
	must(t, st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"))
	must(t, st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "Todo"}, {GID: "s2", Name: "Done"}}))
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "残る", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}},
		Tags:        []asana.Ref{{GID: "g1", Name: "急ぎ"}}}}))
	must(t, st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ"}}))
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "未送信"}, testNow))
	rollbackTo(t, st.db, idx)
	st.Close()

	st, err = Open(ctx, path)
	must(t, err)
	defer st.Close()
	d, err := st.GetTask(ctx, "t1")
	must(t, err)
	if d.Name != "未送信" || len(d.Memberships) != 1 || d.Memberships[0].SectionGID != "s1" {
		t.Errorf("task after migration: %+v", d)
	}
	tags, err := st.ListTags(ctx)
	must(t, err)
	if len(tags) != 1 || tags[0].Name != "急ぎ" || tags[0].Color != "" {
		t.Errorf("tags = %+v", tags)
	}
	ops, err := st.ListOps(ctx)
	must(t, err)
	if len(ops) != 1 {
		t.Errorf("outbox = %+v", ops)
	}
	if secs, err := st.ListMyTaskSections(ctx, "w1"); err != nil || len(secs) != 0 {
		t.Errorf("my sections = %v, %v", secs, err)
	}
	// Opening again is a no-op.
	st.Close()
	st, err = Open(ctx, path)
	must(t, err)
}
