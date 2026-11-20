package sync

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/store"
)

// unreadableWorld is setupPush with the store's path, to queue entries as a newer
// version (or a damaged store) would have.
func unreadableWorld(t *testing.T) (*writeServer, *Syncer, *store.Store, string) {
	t.Helper()
	ws := &writeServer{t: t, tasks: map[string]*serverTask{"t1": {Name: "A", Project: "p1", Section: "s1"}}}
	srv := httptest.NewServer(ws)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	st, err := store.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeGID(ctx, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	return ws, &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}}, st, path
}

// queueRaw adds an outbox entry as it is stored, and returns its id.
func queueRaw(t *testing.T, path, kind, task, payload, state string, verify bool) int64 {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	res, err := db.Exec(`INSERT INTO outbox(kind, task_gid, payload, state, verify, created_at) VALUES(?, ?, ?, ?, ?, '2026-11-01T00:00:00Z')`,
		kind, task, payload, state, verify)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// An entry this version cannot read (a kind of a newer version, a key or a type it does
// not know) is not sent and not failed: it stays pending, with a note, and is looked at
// again on the next push. The entries after it are sent.
func TestPushHoldsUnreadableEntries(t *testing.T) {
	ws, s, st, path := unreadableWorld(t)
	ctx := context.Background()
	held := map[int64]string{
		queueRaw(t, path, "from_the_future", "t1", `{"x":1}`, "pending", false):                                     "この版の asagao では送れない種類",
		queueRaw(t, path, "from_the_future", "t1", `{}`, "pending", true):                                           "この版の asagao では送れない種類", // to be verified: nothing to look for
		queueRaw(t, path, "update_task", "t9", `{"due_at":"2026-11-20T10:00:00Z"}`, "pending", false):               "内容を読めない",
		queueRaw(t, path, "add_tag", "t1", `{"tag":5,"tag_name":"x","workspace":"w1"}`, "pending", false):           "内容を読めない",
		queueRaw(t, path, "add_comment", "t1", `{"text":"x","story_gid":"local:s","html_text":7}`, "pending", true): "内容を読めない",
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "送る"}, now); err != nil {
		t.Fatal(err)
	}
	for round := 1; round <= 2; round++ {
		res, err := s.Push(ctx)
		if err != nil || res.Sent != 1 && round == 1 || res.Failed != 0 {
			t.Fatalf("push %d = %+v, %v", round, res, err)
		}
		if res.Remaining != len(held) {
			t.Errorf("push %d: %d remaining, want %d", round, res.Remaining, len(held))
		}
		for id, note := range held {
			op, err := st.GetOp(ctx, id)
			if err != nil {
				t.Fatalf("entry %d: %v", id, err)
			}
			if op.State != outbox.StatePending || op.Attempts != 0 || !strings.Contains(op.LastError, note) {
				t.Errorf("push %d: entry %d (%s) = %s, %d attempts, %q", round, id, op.Kind, op.State, op.Attempts, op.LastError)
			}
		}
	}
	if ws.tasks["t1"].Name != "送る" {
		t.Errorf("the readable entry was not sent: %+v", ws.tasks["t1"])
	}
	for _, c := range ws.calls {
		if strings.Contains(c, "due_at") || strings.Contains(c, "/tags") || strings.Contains(c, "/stories") {
			t.Errorf("an unreadable entry was sent: %s", c)
		}
	}
}

// An entry of an unknown kind left "sending" (a newer version stopped while sending it)
// may have been a create: it is checked, not sent again — and held, as nothing can be
// checked for it.
func TestInterruptedUnknownKindIsHeld(t *testing.T) {
	_, s, st, path := unreadableWorld(t)
	ctx := context.Background()
	id := queueRaw(t, path, "create_something", "t1", `{"name":"x"}`, "sending", false)
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := st.GetOp(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != outbox.StatePending || !op.Verify || !strings.Contains(op.LastError, "この版の asagao では送れない種類") {
		t.Fatalf("entry = %s verify=%v %q", op.State, op.Verify, op.LastError)
	}
}

// Sending an entry whose payload turns out unreadable (holdUnreadable is checked first, so
// only a handler's own decode) puts it back unsent and held.
func TestUnreadableWhileSendingIsHeld(t *testing.T) {
	_, s, st, path := unreadableWorld(t)
	ctx := context.Background()
	id := queueRaw(t, path, "add_follower", "t1", `{"user":1}`, "pending", false)
	if ok, err := st.BeginSend(ctx, id, now); err != nil || !ok {
		t.Fatalf("BeginSend = %v, %v", ok, err)
	}
	op, err := st.GetOp(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	op.Attempts++
	if err := st.CountAttempt(ctx, id, now); err != nil {
		t.Fatal(err)
	}
	var res PushResult
	out, err := s.Pusher().send(ctx, *op)
	if _, err := s.Pusher().handleOutcome(ctx, *op, out, err, false, &res, &dropState{res: &res}); err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Errorf("counted as failed: %+v", res)
	}
	op, err = st.GetOp(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if op.State != outbox.StatePending || op.Attempts != 0 || !strings.Contains(op.LastError, "内容を読めない") {
		t.Fatalf("entry = %s, %d attempts, %q", op.State, op.Attempts, op.LastError)
	}
}
