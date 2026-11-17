package store

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/asana"
)

// rollbackTo turns a fresh store back into one written before migration idx (and every
// later one): it drops the tables and columns those migrations add and sets user_version.
// Data the later migrations only deleted (e.g. sync_state rows) is not restored.
func rollbackTo(t *testing.T, db *sql.DB, idx int) {
	t.Helper()
	ctx := context.Background()
	table := regexp.MustCompile(`(?i)CREATE (?:VIRTUAL )?TABLE (\w+)`)
	column := regexp.MustCompile(`(?i)ALTER TABLE (\w+) ADD COLUMN (\w+)`)
	var stmts []string
	for i := len(migrations) - 1; i >= idx; i-- {
		var add []string
		for _, m := range column.FindAllStringSubmatch(migrations[i], -1) {
			add = append(add, fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", m[1], m[2]))
		}
		for _, m := range table.FindAllStringSubmatch(migrations[i], -1) {
			add = append(add, "DROP TABLE IF EXISTS "+m[1])
		}
		slices.Reverse(add)
		stmts = append(stmts, add...)
	}
	stmts = append(stmts, fmt.Sprintf("PRAGMA user_version = %d", idx))
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// A store from before the inbox and the custom fields (main's user_version 12) gets every
// later table, keeps its data and unsent changes, and does a full sync next.
func TestMigrateStoreFromBeforeInboxAndFields(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/store.db"
	st, err := Open(ctx, path)
	must(t, err)
	must(t, st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "残るタスク"}}))
	must(t, st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "未送信の変更"}, testNow))
	rollbackTo(t, st.db, 12)
	must(t, st.SetSyncState(ctx, "last_full_sync", "2026-11-01T00:00:00Z"))
	st.Close()

	st, err = Open(ctx, path)
	must(t, err)
	defer st.Close()
	for _, table := range []string{"notifications", "custom_fields", "project_custom_fields", "task_custom_fields", "task_dependencies", "task_refs"} {
		var n int
		must(t, st.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n))
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	if d, err := st.GetTask(ctx, "t1"); err != nil || d.Name != "未送信の変更" || d.ApprovalStatus != "" {
		t.Fatalf("task %+v %v", d, err)
	}
	if ops, _ := st.PendingOps(ctx); len(ops) != 1 {
		t.Fatalf("queued change lost: %+v", ops)
	}
	if v, _ := st.SyncState(ctx, "last_full_sync"); v != "" {
		t.Fatalf("the next sync must be a full one (last_full_sync = %q)", v)
	}
}
