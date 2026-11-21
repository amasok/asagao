package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

// setupStartOverride is setupStart with a front handler: a PUT /tasks/t1 or POST /tasks
// whose body has start_on is answered with (status, body) when status != 0, instead of by
// the startServer. calls counts those requests.
func setupStartOverride(t *testing.T, task map[string]any, status *int, body *string, calls *int) (*startServer, *Syncer, *store.Store) {
	t.Helper()
	f := &startServer{t: t, task: map[string]any{"gid": "t1", "name": "設計", "workspace": map[string]any{"gid": "w1"},
		"modified_at": "2026-11-13T00:00:00Z"}}
	for k, v := range task {
		f.task[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		var in struct{ Data map[string]any }
		_ = json.Unmarshal(b, &in)
		if _, ok := in.Data["start_on"]; ok && *status != 0 && r.Method != http.MethodGet {
			*calls++
			rw.WriteHeader(*status)
			fmt.Fprint(rw, *body)
			return
		}
		f.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	if err := st.SetMeGID(ctx, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(f.task)
	var mt asana.Task
	if err := json.Unmarshal(b, &mt); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{mt}); err != nil {
		t.Fatal(err)
	}
	return f, &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}}, st
}

const milestoneBody = `{"errors":[{"error":"cannot_set_start_date_on_milestone","message":"You cannot set a start date on a milestone."}]}`

// A 400 whose code is not the milestone one, or has none, and a milestone code under
// another status, are not taken for the milestone's: the entry stays failed in the outbox
// and the cache keeps the edit. (402 is the plan's and is not in this list.)
func TestAttackMilestoneCodeOnlyWith400(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"400 other code", 400, `{"errors":[{"error":"invalid_request","message":"You cannot set a start date on a milestone."}]}`},
		{"400 no code", 400, `{"errors":[{"message":"You cannot set a start date on a milestone."}]}`},
		{"400 empty body", 400, ``},
		{"400 empty errors", 400, `{"errors":[]}`},
		{"400 code in message only", 400, `{"errors":[{"message":"cannot_set_start_date_on_milestone"}]}`},
		{"400 code case differs", 400, `{"errors":[{"error":"CANNOT_SET_START_DATE_ON_MILESTONE"}]}`},
		{"400 code with suffix", 400, `{"errors":[{"error":"cannot_set_start_date_on_milestone_x"}]}`},
		{"403 same code", 403, milestoneBody},
		{"422 same code", 422, milestoneBody},
		{"409 same code", 409, milestoneBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body, calls := tc.status, tc.body, 0
			_, s, st := setupStartOverride(t, map[string]any{"due_on": "2026-12-10"}, &status, &body, &calls)
			ctx := context.Background()
			s.now = func() time.Time { return now }
			if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
				t.Fatal(err)
			}
			res, err := s.Push(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Discarded) != 0 || res.Failed != 1 {
				t.Fatalf("push = %+v (calls %d)", res, calls)
			}
			if ops, _ := st.ListOps(ctx); len(ops) != 1 {
				t.Fatalf("ops = %+v", ops)
			}
			if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "2026-12-01" {
				t.Fatalf("cache start_on = %q, the edit was lost", d.StartOn)
			}
			noStartMark(t, st)
		})
	}
}

// A mixed errors list that has the code among others is the milestone's answer.
func TestAttackMilestoneCodeAmongErrors(t *testing.T) {
	status := 400
	body := `{"errors":[{"error":"invalid_request","message":"x"},{"error":"cannot_set_start_date_on_milestone","message":"y"}]}`
	calls := 0
	f, s, st := setupStartOverride(t, map[string]any{"due_on": "2026-12-10"}, &status, &body, &calls)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "N", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	// the server refuses the first PUT only; the retry has no start_on and goes to startServer
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 || calls != 1 {
		t.Fatalf("push = %+v, %v calls %d", res, err, calls)
	}
	if f.task["name"] != "N" {
		t.Fatalf("server task = %v", f.task)
	}
	noStartMark(t, st)
}

// An edit made after the start date, folded into the same entry, is sent with the rest;
// the cache shows Asana's state (no start date) and keeps the later edit.
func TestAttackMilestoneFoldedLaterEdits(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	for _, ch := range []map[string]any{
		{"start_on": "2026-12-01"},
		{"name": "後の名前"},
		{"due_on": "2026-12-12"},
		{"notes": "メモ"},
	} {
		if err := st.UpdateTaskLocal(ctx, "t1", ch, now); err != nil {
			t.Fatal(err)
		}
	}
	f.task["resource_subtype"] = "milestone"
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if f.task["name"] != "後の名前" || f.task["due_on"] != "2026-12-12" || f.task["notes"] != "メモ" || f.task["start_on"] != nil {
		t.Fatalf("server task = %v", f.task)
	}
	if last := f.puts[len(f.puts)-1]; func() bool { _, ok := last["start_on"]; return ok }() {
		t.Fatalf("the second PUT still has start_on: %v", last)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.Name != "後の名前" || d.StartOn != "" || d.DueOn != "2026-12-12" {
		t.Fatalf("t1 = %+v", d)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	noStartMark(t, st)
}

// 404 and 500 with the milestone code keep their own handling (deleted / retried); they
// are never reported as the milestone's.
func TestAttackMilestoneCode404And500(t *testing.T) {
	for _, status := range []int{404, 500} {
		body, calls, st0 := milestoneBody, 0, status
		_, s, st := setupStartOverride(t, map[string]any{"due_on": "2026-12-10"}, &st0, &body, &calls)
		ctx := context.Background()
		s.now = func() time.Time { return now }
		if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
			t.Fatal(err)
		}
		res, _ := s.Push(ctx)
		for _, d := range res.Discarded {
			if strings.Contains(d.Reason, "マイルストーン") {
				t.Fatalf("%d: taken for the milestone's: %+v", status, d)
			}
		}
		noStartMark(t, st)
	}
}

// The cache had a start date (an older one); the edit set a new one; Asana says milestone:
// the cache ends with the start date Asana has (2026-11-01 here), not with the edited one.
func TestAttackMilestoneCacheMatchesAsana(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": "2026-11-01"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.task["resource_subtype"] = "milestone"
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "2026-11-01" {
		t.Fatalf("cache start_on = %q, Asana has 2026-11-01", d.StartOn)
	}
}

// A task with a start date is not made a milestone, and a start date is not queued on a
// milestone made locally; the type edit with the start date cleared goes through, and is sent.
func TestAttackMilestoneTypeAndStartLocal(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "resource_subtype": "default_task"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone"}, now); err == nil {
		t.Fatal("a task with a queued start date was made a milestone")
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone", "start_on": ""}, now); err != nil {
		t.Fatalf("type with the start date cleared: %v", err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 0 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if f.task["resource_subtype"] != "milestone" || f.task["start_on"] != nil {
		t.Fatalf("server = %v", f.task)
	}
}

// The cache does not know the type; Asana's GET says milestone: the start date is not sent
// at all (no PUT), the rest is, the reason is given, and the cache learns the type so a
// next start date is refused before it is queued.
func TestAttackMilestoneBlockedByGET(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "N", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.task["resource_subtype"] = "milestone"
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 || !strings.Contains(res.Discarded[0].Reason, "マイルストーン") {
		t.Fatalf("push = %+v, %v", res, err)
	}
	for _, p := range f.puts {
		if _, ok := p["start_on"]; ok {
			t.Fatalf("a PUT carried start_on: %v", f.puts)
		}
	}
	if f.task["name"] != "N" {
		t.Fatalf("server = %v", f.task)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-02"}, now); err == nil {
		t.Fatal("the cache did not learn that t1 is a milestone")
	}
	noStartMark(t, st)
}

// Only the start date, GET says milestone: no PUT at all, the entry is gone.
func TestAttackMilestoneBlockedByGETOnly(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.task["resource_subtype"] = "milestone"
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 || len(f.puts) != 0 {
		t.Fatalf("push = %+v, %v puts %v", res, err, f.puts)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-02"}, now); err == nil {
		t.Fatal("the cache did not learn that t1 is a milestone (no PUT carried it either)")
	}
}

// Clearing a start date of a milestone in Asana is not "blocked" (nothing to refuse).
func TestAttackMilestoneClearNotBlocked(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "start_on": "2026-12-01", "resource_subtype": "milestone"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": ""}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 0 {
		t.Fatalf("push = %+v, %v puts %v", res, err, f.puts)
	}
	if f.task["start_on"] != nil {
		t.Fatalf("server = %v", f.task)
	}
}

// The task is a milestone in Asana, the edit turns it into a plain task with a start date:
// sent (the GET's type does not decide, the edit's does).
func TestAttackMilestoneEditChangesTypeBack(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "resource_subtype": "milestone"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "default_task", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 0 {
		t.Fatalf("push = %+v, %v puts %v", res, err, f.puts)
	}
	if f.task["start_on"] != "2026-12-01" || f.task["resource_subtype"] != "default_task" {
		t.Fatalf("server = %v", f.task)
	}
}

// The task was made a milestone after the GET (milestoneOnWrite): the 400 path takes the
// start date out and sends the rest, no mark.
func TestAttackMilestoneOnWrite400(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	f.milestoneOnWrite = true
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "N", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if len(f.puts) != 2 || f.task["name"] != "N" || f.task["start_on"] != nil {
		t.Fatalf("puts %v server %v", f.puts, f.task)
	}
	noStartMark(t, st)
}

// noWorkspaceStore is a store that does not know the task's workspace.
type noWorkspaceStore struct{ LocalStore }

func (noWorkspaceStore) TaskWorkspace(context.Context, string) (string, error) {
	return "", store.ErrNotFound
}

// The task's workspace is not known when its entry is sent: the milestone 400 is still taken
// out (the workspace is only for the mark); a 402 is not, and makes no mark.
func TestAttackMilestone400UnknownWorkspace(t *testing.T) {
	for _, milestone := range []bool{true, false} {
		f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
		f.milestoneOnWrite = milestone
		f.plan402 = !milestone
		s.Store = noWorkspaceStore{st}
		ctx := context.Background()
		s.now = func() time.Time { return now }
		if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "N", "start_on": "2026-12-01"}, now); err != nil {
			t.Fatal(err)
		}
		res, err := s.Push(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if milestone && (res.Failed != 0 || len(res.Discarded) != 1 || f.task["name"] != "N" || f.task["start_on"] != nil) {
			t.Fatalf("milestone: push = %+v (puts %v)", res, f.puts)
		}
		if !milestone && res.Failed != 1 {
			t.Fatalf("402: push = %+v", res)
		}
		noStartMark(t, st)
	}
}

// After the milestone is blocked (cache learned the type), a plain task with a start date
// queued later and a 402 is recorded as usual.
func TestAttackMilestoneThenPlan402(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	f.task["resource_subtype"] = "milestone"
	if res, err := s.Push(ctx); err != nil || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	noStartMark(t, st)
	// Asana turned the task back into a plain one; the edit says so too.
	f.task["resource_subtype"] = "default_task"
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "default_task", "start_on": "2026-12-02"}, now); err != nil {
		t.Fatalf("type back with a start date: %v", err)
	}
	f.plan402 = true
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || !strings.Contains(res.Discarded[0].Reason, "タスクの開始日") {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.StartOn, "w1"); !ok {
		t.Fatal("402 not recorded")
	}
}

// A create made a milestone locally (no start date) keeps its folded edits and type; a
// start date on it is refused. A create with a start date, whose task is a milestone in
// Asana by a rule (milestoneOnWrite), is created again without it.
func TestAttackMilestoneCreateFoldedLaterEdit(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "リリース", ProjectGID: "p1", DueOn: "2026-12-10"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []map[string]any{{"resource_subtype": "milestone"}, {"name": "リリース改"}, {"notes": "n"}} {
		if err := st.UpdateTaskLocal(ctx, gid, ch, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"start_on": "2026-12-02"}, now); err == nil {
		t.Fatal("a start date was queued on a local milestone")
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 0 {
		t.Fatalf("push = %+v, %v (created %v)", res, err, f.created)
	}
	if f.created["name"] != "リリース改" || f.created["resource_subtype"] != "milestone" {
		t.Fatalf("created %v", f.created)
	}

	f2, s2, st2 := setupStart(t, nil)
	f2.milestoneOnWrite = true
	s2.now = func() time.Time { return now }
	if _, err := st2.CreateTaskLocal(ctx, store.NewTask{Name: "x", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err = s2.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 {
		t.Fatalf("create push = %+v, %v (created %v)", res, err, f2.created)
	}
	if _, ok := f2.created["start_on"]; ok || f2.created["name"] != "x" {
		t.Fatalf("created %v", f2.created)
	}
	noStartMark(t, st2)
}

// A create refused with another 400 stays failed (no create without the start date).
func TestAttackMilestoneCreateOther400Fails(t *testing.T) {
	status, body, calls := 400, `{"errors":[{"error":"invalid_request","message":"bad"}]}`, 0
	_, s, st := setupStartOverride(t, nil, &status, &body, &calls)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if _, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "x", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 || len(res.Discarded) != 0 || calls != 1 {
		t.Fatalf("push = %+v, %v calls %d", res, err, calls)
	}
	noStartMark(t, st)
}

// The milestone 400 keeps coming even without a start date in the retry (a broken
// server): the push must end, not loop.
func TestAttackMilestoneEndlessServerTerminates(t *testing.T) {
	status, body, calls := 400, milestoneBody, 0
	_, s, st := setupStartOverride(t, map[string]any{"due_on": "2026-12-10"}, &status, &body, &calls)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "N", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.Push(ctx) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("push did not end")
	}
	if calls > 3 {
		t.Fatalf("%d requests with start_on", calls)
	}
}
