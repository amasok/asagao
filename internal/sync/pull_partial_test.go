package sync

import (
	"context"
	"database/sql"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// projectTasksJSON is tasks t<from>..t<to> of project p1, as GET /tasks?project=p1 returns them.
func projectTasksJSON(from, to int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := from; i <= to; i++ {
		if i > from {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"gid":"t%d","name":"T%d","memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}],"tags":[{"gid":"g1","name":"urgent"}]}`, i, i)
	}
	b.WriteString("]")
	return b.String()
}

// A full sync that fails while it writes a project's tasks (#55: they are written in
// several transactions) prunes nothing and records no progress: a task gone from Asana, and
// tasks not yet written again, are still there, and the last sync, the last full sync and
// the events tokens are as before. The next sync then ends as one that never failed.
func TestFullSyncFailingWhileWritingTasksKeepsTheCache(t *testing.T) {
	fake := &fakeAsana{t: t, projectTasks: projectTasksJSON(2, 40)}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	s := &Syncer{API: api, Store: st, Cfg: config.SyncConfig{CompletedDays: 14}, now: func() time.Time { return now }}
	if _, err := s.Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	keys := []string{store.KeyLastSync, keyLastFullSync, keyLastPoll, store.KeyEvents + "p1"}
	state := func() map[string]string {
		m := map[string]string{}
		for _, k := range keys {
			v, err := st.SyncState(ctx, k)
			if err != nil {
				t.Fatal(err)
			}
			m[k] = v
		}
		return m
	}
	before := state()
	if before[store.KeyLastSync] == "" || before[keyLastFullSync] == "" {
		t.Fatalf("first sync recorded %v", before)
	}

	// Another process (the test) makes writing t30 fail; t2 is gone from Asana.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_t30 BEFORE UPDATE ON tasks WHEN NEW.gid = 't30'
		BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	fake.projectTasks = projectTasksJSON(3, 40)
	if _, err := s.Run(ctx, true); err == nil {
		t.Fatal("the sync succeeded though writing t30 failed")
	}
	for _, gid := range []string{"t1", "t2", "t3", "t29", "t30", "t40"} {
		if _, err := st.GetTask(ctx, gid); err != nil {
			t.Errorf("%s after the failed sync: %v", gid, err)
		}
	}
	if got := state(); fmt.Sprint(got) != fmt.Sprint(before) {
		t.Fatalf("the failed sync recorded progress: %v, was %v", got, before)
	}

	if _, err := db.ExecContext(ctx, "DROP TRIGGER fail_t30"); err != nil {
		t.Fatal(err)
	}
	res, err := s.Run(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Pruned != 1 {
		t.Fatalf("pruned %d, want 1 (t2)", res.Pruned)
	}
	if _, err := st.GetTask(ctx, "t2"); err == nil {
		t.Fatal("t2, gone from Asana, still stored after the sync that succeeded")
	}
	stats, err := st.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Tasks != 40 { // t1, t1a, t3..t40
		t.Fatalf("%d tasks stored, want 40", stats.Tasks)
	}
	if got := state(); got[store.KeyLastSync] != now.Format(time.RFC3339) || got[keyLastFullSync] != now.Format(time.RFC3339) {
		t.Fatalf("after the sync that succeeded: %v", got)
	}
}
