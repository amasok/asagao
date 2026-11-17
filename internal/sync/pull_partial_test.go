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

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/store"
)

// projectTasksJSON is tasks t<from>..t<to> of project p1, named <name><i>, as
// GET /tasks?project=p1 returns them.
func projectTasksJSON(name string, from, to int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := from; i <= to; i++ {
		if i > from {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"gid":"t%d","name":"%s%d","memberships":[{"project":{"gid":"p1"},"section":{"gid":"s1"}}],"tags":[{"gid":"g1","name":"urgent"}]}`, i, name, i)
	}
	b.WriteString("]")
	return b.String()
}

// A full sync that fails part way through writing a project's tasks, after some of them
// were written in transactions of their own (#55), prunes nothing and records no progress:
// the tasks written before the failure have what Asana returned, a task gone from Asana and
// the tasks not yet written are still there as they were, and the last sync, the last full
// sync, the last poll and the events token are as before. The next sync then ends as one
// that never failed.
func TestFullSyncFailingWhileWritingTasksKeepsTheCache(t *testing.T) {
	defer store.SetTaskWriteSlicesForTest(0, time.Millisecond)() // one task per transaction
	fake := &fakeAsana{t: t, projectTasks: projectTasksJSON("T", 2, 40)}
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
	fake.projectTasks = projectTasksJSON("U", 3, 40) // renamed in Asana
	if _, err := s.Run(ctx, true); err == nil {
		t.Fatal("the sync succeeded though writing t30 failed")
	}
	name := func(gid string) string {
		t.Helper()
		d, err := st.GetTask(ctx, gid)
		if err != nil {
			t.Fatalf("%s: %v", gid, err)
		}
		return d.Name
	}
	for i := 2; i <= 40; i++ {
		gid := fmt.Sprintf("t%d", i)
		want := fmt.Sprintf("T%d", i) // gone from Asana (t2), or not written again (t30..)
		if i >= 3 && i < 30 {
			want = fmt.Sprintf("U%d", i) // written before the failure
		}
		if got := name(gid); got != want {
			t.Errorf("%s after the failed sync: %q, want %q", gid, got, want)
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
	for i := 3; i <= 40; i++ {
		if got, want := name(fmt.Sprintf("t%d", i)), fmt.Sprintf("U%d", i); got != want {
			t.Errorf("t%d after the sync that succeeded: %q, want %q", i, got, want)
		}
	}
	if got := state(); got[store.KeyLastSync] != now.Format(time.RFC3339) || got[keyLastFullSync] != now.Format(time.RFC3339) {
		t.Fatalf("after the sync that succeeded: %v", got)
	}
}
