package sync

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/outbox"
)

func opState(t *testing.T, s *Syncer, id int64) *outbox.Op {
	t.Helper()
	op, err := s.Store.GetOp(context.Background(), id)
	if err != nil {
		t.Fatalf("entry %d: %v", id, err)
	}
	return op
}

// An unreadable update among updates to other existing tasks (a batch) neither stops the
// push nor goes out in the batch; the readable ones are sent.
func TestAttackUnreadableInsideBatchGroup(t *testing.T) {
	ws, s, st, path := unreadableWorld(t)
	ctx := context.Background()
	for _, g := range []string{"t2", "t3", "t4"} {
		ws.tasks[g] = &serverTask{Name: g, Project: "p1", Section: "s1"}
		if err := st.UpsertTasks(ctx, []asana.Task{{GID: g, Name: g, Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "A2"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t4", map[string]any{"name": "D2"}, now); err != nil {
		t.Fatal(err)
	}
	bad := queueRaw(t, path, "update_task", "t2", `{"name":"B2","due_at":"2026-11-20"}`, "pending", false)
	badType := queueRaw(t, path, "update_task", "t3", `{"completed":"yes"}`, "pending", false)
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Failed != 0 || res.Sent != 2 {
		t.Errorf("res = %+v", res)
	}
	for _, id := range []int64{bad, badType} {
		op := opState(t, s, id)
		if op.State != outbox.StatePending || op.Attempts != 0 || op.LastError == "" {
			t.Errorf("held entry %d = %s %d %q", id, op.State, op.Attempts, op.LastError)
		}
	}
	if n, _ := st.PendingOps(ctx); len(n) != 2 {
		t.Errorf("%d entries queued, want the 2 held", len(n))
	}
	if ws.tasks["t1"].Name != "A2" || ws.tasks["t4"].Name != "D2" || ws.tasks["t2"].Name != "t2" || ws.tasks["t3"].Name != "t3" {
		t.Errorf("tasks = %+v %+v %+v %+v", ws.tasks["t1"], ws.tasks["t2"], ws.tasks["t3"], ws.tasks["t4"])
	}
}

// A mark_notification this version cannot read is not sent along with a readable one.
func TestAttackUnreadableMarkNotificationNotSentAlong(t *testing.T) {
	ws, s, st, path := unreadableWorld(t)
	ctx := context.Background()
	var marked [][]string
	ws.hook = func(rw http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.URL.Path, "/users/me/notifications/mark") {
			return false
		}
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Data struct {
				GIDs []string `json:"notification_gids"`
			}
		}
		_ = json.Unmarshal(b, &body)
		marked = append(marked, body.Data.GIDs)
		rw.Write([]byte(`{"data":{"failed_gids":[]}}`))
		return true
	}
	ok := queueRaw(t, path, "mark_notification", "n1", `{"workspace":"w1","name":"a","read":true}`, "pending", false)
	extra := queueRaw(t, path, "mark_notification", "n2", `{"workspace":"w1","name":"b","read":true,"later_key":1}`, "pending", false)
	wrongType := queueRaw(t, path, "mark_notification", "n3", `{"workspace":"w1","name":2,"read":true}`, "pending", false)
	ok2 := queueRaw(t, path, "mark_notification", "n4", `{"workspace":"w1","name":"d","read":true}`, "pending", false)
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	for _, g := range marked {
		for _, x := range g {
			if x == "n2" || x == "n3" {
				t.Errorf("unreadable notification %s was marked: %v", x, marked)
			}
		}
	}
	for _, id := range []int64{extra, wrongType} {
		op := opState(t, s, id)
		if op.State != outbox.StatePending || op.LastError == "" || op.Attempts != 0 {
			t.Errorf("entry %d = %s %q %d", id, op.State, op.LastError, op.Attempts)
		}
	}
	for _, id := range []int64{ok, ok2} {
		if _, err := st.GetOp(ctx, id); err == nil {
			t.Errorf("readable entry %d not sent", id)
		}
	}
}

// An unknown kind left "sending" and one of a known kind left "sending" with an
// unreadable payload are held without a request; the push does not fail.
func TestAttackInterruptedUnreadableKnownKind(t *testing.T) {
	ws, s, st, path := unreadableWorld(t)
	ctx := context.Background()
	id := queueRaw(t, path, "update_task", "t1", `{"name":"x","from_future":true}`, "sending", false)
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	op := opState(t, s, id)
	if op.State != outbox.StatePending || res.Failed != 0 || op.LastError == "" {
		t.Fatalf("entry = %s %q res=%+v", op.State, op.LastError, res)
	}
	for _, c := range ws.calls {
		if strings.Contains(c, "from_future") {
			t.Errorf("sent: %s", c)
		}
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if op2 := opState(t, s, id); op2.Attempts != op.Attempts || op2.State != outbox.StatePending {
		t.Errorf("second push changed it: %+v", op2)
	}
	_ = st
}

// A held create keeps what depends on it waiting (a later change to the local task is not
// sent either), and the held entry is not sent after a hold note exists already.
func TestAttackHeldCreateBlocksItsFollowers(t *testing.T) {
	ws, s, _, path := unreadableWorld(t)
	ctx := context.Background()
	c := queueRaw(t, path, "create_task", "local:x1", `{"workspace":"w1","name":"n","future":1}`, "pending", false)
	f := queueRaw(t, path, "update_task", "local:x1", `{"name":"later"}`, "pending", false)
	for i := 0; i < 2; i++ {
		if _, err := s.Push(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []int64{c, f} {
		if op := opState(t, s, id); op.State != outbox.StatePending || op.Attempts != 0 {
			t.Errorf("entry %d = %s %d %q", id, op.State, op.Attempts, op.LastError)
		}
	}
	for _, call := range ws.calls {
		if strings.Contains(call, "POST /tasks") || strings.Contains(call, "later") {
			t.Errorf("sent: %s", call)
		}
	}
}

// Null / huge / fractional values: none of them is sent half-read or panics.
func TestAttackOddValuesAreHeldOrRead(t *testing.T) {
	_, s, _, path := unreadableWorld(t)
	cases := []struct {
		kind, payload string
		held          bool
	}{
		{"add_time_entry", `{"entry":"local:e","duration_minutes":1.5,"entered_on":"2026-11-20"}`, true},
		{"add_time_entry", `{"entry":"local:e","duration_minutes":9223372036854775808,"entered_on":"2026-11-20"}`, true},
		{"add_time_entry", `{"entry":"local:e","duration_minutes":2.0,"entered_on":"2026-11-20"}`, false},
		{"update_task", `{"name":null,"due_on":null}`, false},
		{"create_task", `{"workspace":"w1","name":"t","custom_fields":{"a":{"b":{"c":[1,{"d":null}]}}}}`, false},
		{"create_task", `{"workspace":"w1","name":"t","custom_fields":null}`, false},
		{"create_task", `{"workspace":"w1","name":"t","custom_fields":[]}`, true},
		{"set_custom_field", `{"field":"f","type":"x","value":{"a":1},"display":""}`, false},
		{"create_section", `{"project":"p1","name":"n","existing":null}`, false},
	}
	for _, tc := range cases {
		op := outbox.Op{Kind: outbox.Kind(tc.kind), Payload: map[string]any{}}
		if err := json.Unmarshal([]byte(tc.payload), &op.Payload); err != nil {
			t.Fatal(err)
		}
		err := outbox.Readable(op)
		if (err != nil) != tc.held {
			t.Errorf("%s %s: Readable = %v, held want %v", tc.kind, tc.payload, err, tc.held)
		}
	}
	_ = s
	_ = path
}
