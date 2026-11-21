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

// The type is changed to milestone and the start date set in two steps while the cache
// does not know the type is a milestone: only the type edit is sent through; the 400 for
// the start date is dropped and the type (which Asana took) stays.
func TestAttackMilestoneTypeAndStartTogetherInAsana(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10", "resource_subtype": "default_task"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	// start date first (task is a plain task in the cache), then the type
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "milestone"}, now); err != nil {
		t.Fatalf("type change after a queued start date: %v", err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (puts %v)", res, err, f.puts)
	}
	if f.task["resource_subtype"] != "milestone" || f.task["start_on"] != nil {
		t.Fatalf("server = %v", f.task)
	}
	if d, _ := st.GetTask(ctx, "t1"); d.StartOn != "" {
		t.Fatalf("cache start_on = %q", d.StartOn)
	}
}

// A 402 after a milestone 400 on another entry in the same push still marks the workspace
// (the milestone's 400 must not swallow the plan's mark), and a milestone 400 does not
// hide start dates for a later edit of an ordinary task.
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
	// Asana turns the task back into a plain one; a start date can be queued again (no
	// workspace mark) and a 402 now is recorded as usual.
	f.task["resource_subtype"] = "default_task"
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"start_on": "2026-12-02"}, now); err != nil {
		t.Fatalf("start date queued after a milestone 400: %v", err)
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

// A task created with a start date, with a custom type in the same entry, refused 400
// milestone: both are dropped in turn and the create completes with the name and due date.
func TestAttackMilestoneWithCustomTypeChange(t *testing.T) {
	f, s, st := setupStart(t, map[string]any{"due_on": "2026-12-10"})
	ctx := context.Background()
	s.now = func() time.Time { return now }
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"name": "改名", "start_on": "2026-12-01"}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, now); err != nil {
		t.Logf("custom type edit refused locally (%v); scenario skipped", err)
		return
	}
	f.task["resource_subtype"] = "milestone"
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 0 {
		t.Fatalf("push = %+v (puts %v)", res, f.puts)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v (puts %v)", ops, f.puts)
	}
	if f.task["name"] != "改名" || f.task["start_on"] != nil {
		t.Fatalf("server = %v", f.task)
	}
	noStartMark(t, st)
}

// A create (offline gid) refused 400 milestone and then the retry succeeds without the
// start date; the folded later edit is kept and the local gid is mapped.
func TestAttackMilestoneCreateFoldedLaterEdit(t *testing.T) {
	f, s, st := setupStart(t, nil)
	ctx := context.Background()
	s.now = func() time.Time { return now }
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{Name: "リリース", ProjectGID: "p1", DueOn: "2026-12-10", StartOn: "2026-12-01"}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range []map[string]any{{"resource_subtype": "milestone"}, {"name": "リリース改"}, {"notes": "n"}} {
		if err := st.UpdateTaskLocal(ctx, gid, ch, now); err != nil {
			t.Fatal(err)
		}
	}
	// a start date on the local milestone is refused before it is queued
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"start_on": "2026-12-02"}, now); err == nil {
		t.Fatal("a start date was queued on a local milestone")
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 || len(res.Discarded) != 1 {
		t.Fatalf("push = %+v, %v (created %v)", res, err, f.created)
	}
	if _, ok := f.created["start_on"]; ok || f.created["name"] != "リリース改" || f.created["resource_subtype"] != "milestone" {
		t.Fatalf("created %v", f.created)
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("ops = %+v", ops)
	}
	noStartMark(t, st)
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
