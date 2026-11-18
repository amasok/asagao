package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// Attack tests for sending custom task types (#59): a task made offline, a flaky server,
// refusals that must not be sent again, the plan's 402 per workspace, concurrent pushes.

type atkServer struct {
	mu     stdsync.Mutex
	log    []string // "METHOD path"
	bodies []map[string]any
	status map[string]int // "METHOD path" -> status to answer (once the count is used up: normal)
	left   map[string]int // remaining times to answer status
	// state of the created task t9
	subtype, typ, st string
	name             string
}

func (f *atkServer) fail(key string, code, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[key], f.left[key] = code, times
}

func (f *atkServer) taskJSON(gid string) string {
	ct, cs := "null", "null"
	if f.subtype == "custom" {
		ct = fmt.Sprintf(`{"gid":%q,"name":"バグ"}`, f.typ)
		cs = fmt.Sprintf(`{"gid":%q,"name":"対応中"}`, f.st)
	}
	return fmt.Sprintf(`{"gid":%q,"name":%q,"resource_subtype":%q,"custom_type":%s,"custom_type_status_option":%s,
		"workspace":{"gid":"w1"},"memberships":[{"project":{"gid":"p1"}}],"modified_at":"2026-11-13T00:00:00Z"}`, gid, f.name, f.subtype, ct, cs)
}

func (f *atkServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	f.log = append(f.log, key)
	var body struct {
		Data map[string]any `json:"data"`
	}
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &body)
	if r.Method != http.MethodGet {
		f.bodies = append(f.bodies, body.Data)
	}
	if f.left[key] > 0 {
		f.left[key]--
		w.WriteHeader(f.status[key])
		fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
		return
	}
	switch {
	case key == "POST /tasks":
		f.subtype = "default_task"
		f.name, _ = body.Data["name"].(string)
		fmt.Fprintf(w, `{"data":%s}`, f.taskJSON("t9"))
	case key == "PUT /tasks/t9":
		if v, ok := body.Data["resource_subtype"].(string); ok {
			f.subtype = v
		}
		if v, ok := body.Data["custom_type"].(string); ok {
			f.typ = v
		}
		if v, ok := body.Data["custom_type_status_option"].(string); ok {
			f.st = v
		}
		if v, ok := body.Data["name"].(string); ok {
			f.name = v
		}
		fmt.Fprintf(w, `{"data":%s}`, f.taskJSON("t9"))
	case key == "GET /tasks/t9":
		fmt.Fprintf(w, `{"data":%s}`, f.taskJSON("t9"))
	case r.URL.Path == "/tasks/t8":
		fmt.Fprint(w, `{"data":{"gid":"t8","name":"y","resource_subtype":"default_task","workspace":{"gid":"w1"},"memberships":[{"project":{"gid":"p1"}}]}}`)
	case r.URL.Path == "/batch":
		w.WriteHeader(http.StatusNotFound)
	default:
		http.NotFound(w, r)
	}
}

func (f *atkServer) count(prefix string) (n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.log {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return
}

func setupAtk(t *testing.T) (*atkServer, *Syncer, *store.Store, *time.Time) {
	t.Helper()
	f := &atkServer{status: map[string]int{}, left: map[string]int{}, subtype: "default_task"}
	srv := httptest.NewServer(f)
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"))
	must(st.UpsertProjects(ctx, "w2", []asana.Project{{GID: "p2", Name: "Q"}}, "me"))
	must(st.ReplaceProjectCustomTypes(ctx, "p1", []asana.CustomType{{GID: "ct1", Name: "バグ", StatusOptions: []asana.CustomTypeStatusOption{
		{GID: "cs-doing", Name: "対応中", CompletionState: "Incomplete"}, {GID: "cs-done", Name: "完了", CompletionState: "Complete"}}}}))
	clock := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	return f, &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}, now: func() time.Time { return clock }}, st, &clock
}

// A task made offline, then given a type, a status and a new name: the create goes first,
// the type and the rest follow in one PUT to the real gid, and the task ends up custom.
func TestAttackOfflineCreateThenType(t *testing.T) {
	f, s, st, _ := setupAtk(t)
	ctx := context.Background()
	gid, err := st.CreateTaskLocal(ctx, store.NewTask{WorkspaceGID: "w1", ProjectGID: "p1", Name: "新規"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"name": "新規2"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"custom_type_status_option": "cs-done"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 0 {
		t.Fatalf("push %+v %v", res, err)
	}
	if f.log[0] != "POST /tasks" {
		t.Fatalf("log %v", f.log)
	}
	if _, bad := f.bodies[0]["custom_type"]; bad {
		t.Fatalf("the create carries a custom type: %v", f.bodies[0])
	}
	if f.subtype != "custom" || f.typ != "ct1" || f.st != "cs-done" || f.name != "新規2" {
		t.Fatalf("server %s %s %s %s; log %v", f.subtype, f.typ, f.st, f.name, f.log)
	}
	if ops, _ := st.PendingOps(ctx); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
	d, err := st.GetTask(ctx, "t9")
	if err != nil || d.CustomType == nil || d.CustomTypeStatus == nil || d.CustomTypeStatus.GID != "cs-done" {
		t.Fatalf("task %+v %v", d, err)
	}
}

// A 500 or 429 for the write is not a refusal: the change stays queued (not failed) and
// is sent once the server is back, exactly once.
func TestAttackTransientErrorsRetry(t *testing.T) {
	for _, code := range []int{500, 502, 429} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f, s, st, _ := setupAtk(t)
			ctx := context.Background()
			if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "x", Workspace: &asana.Ref{GID: "w1"},
				Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			f.fail("PUT /tasks/t9", code, 1000)
			_, _ = s.Push(ctx)
			ops, _ := st.ListOps(ctx)
			if len(ops) != 1 || ops[0].State == "failed" {
				t.Fatalf("after %d: %+v", code, ops)
			}
			if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomTypes, "w1")); v != "" {
				t.Fatalf("%d recorded as the plan's", code)
			}
			f.fail("PUT /tasks/t9", code, 0)
			if _, err := s.Push(ctx); err != nil {
				t.Fatal(err)
			}
			if f.subtype != "custom" {
				t.Fatalf("never applied; log %v", f.log)
			}
			if ops, _ := st.PendingOps(ctx); len(ops) != 0 {
				t.Fatalf("left %+v", ops)
			}
		})
	}
}

// 400/403/422 are refusals: the type is dropped and reported (Discarded), once; nothing
// is sent again, and the task is plain again locally.
func TestAttackRefusalsAreNotRetried(t *testing.T) {
	for _, code := range []int{400, 403, 422} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			f, s, st, _ := setupAtk(t)
			ctx := context.Background()
			if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "x", Workspace: &asana.Ref{GID: "w1"},
				Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
				t.Fatal(err)
			}
			if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			f.fail("PUT /tasks/t9", code, 100)
			var discarded int
			for i := 0; i < 3; i++ {
				res, _ := s.Push(ctx)
				discarded += len(res.Discarded)
			}
			if n := f.count("PUT /tasks/t9"); n != 1 || discarded != 1 {
				t.Fatalf("%d PUTs, %d discarded", n, discarded)
			}
			if ops, _ := st.ListOps(ctx); len(ops) != 0 {
				t.Fatalf("left %+v", ops)
			}
			if d, _ := st.GetTask(ctx, "t9"); d.CustomType != nil || d.ResourceSubtype == "custom" {
				t.Fatalf("still custom locally: %+v", d.TaskSummary)
			}
		})
	}
}

// A rename made together with the refused type is not lost: it is sent again without the
// type, once.
func TestAttackRefusalKeepsTheRest(t *testing.T) {
	f, s, st, _ := setupAtk(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "x", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"name": "改名"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.name = "x"
	f.fail("PUT /tasks/t9", 422, 1)
	res, _ := s.Push(ctx)
	if len(res.Discarded) != 1 || f.name != "改名" || f.subtype == "custom" {
		t.Fatalf("res %+v; server name %q subtype %q; log %v", res, f.name, f.subtype, f.log)
	}
	for _, b := range f.bodies[len(f.bodies)-1:] {
		if _, bad := b["custom_type"]; bad {
			t.Fatalf("the type was sent again: %v", b)
		}
	}
	if ops, _ := st.ListOps(ctx); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
}

// A refused write of a type must not hold up the tasks behind it: another task's rename
// is still sent.
func TestAttackRefusalDoesNotBlockOthers(t *testing.T) {
	f, s, st, _ := setupAtk(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{
		{GID: "t9", Name: "x", Workspace: &asana.Ref{GID: "w1"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}},
		{GID: "t8", Name: "y", Workspace: &asana.Ref{GID: "w1"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t8", map[string]any{"name": "renamed"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.fail("PUT /tasks/t9", 402, 100)
	_, _ = s.Push(ctx)
	if f.count("PUT /tasks/t8")+f.count("POST /batch") == 0 {
		t.Fatalf("the rename was not tried: %v", f.log)
	}
}

// A 402 for a task of workspace w1 is w1's: w2's types are not hidden, and w1's are.
func TestAttackPaymentRequiredPerWorkspace(t *testing.T) {
	f, s, st, _ := setupAtk(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "x", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.fail("PUT /tasks/t9", 402, 100)
	_, _ = s.Push(ctx)
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomTypes, "w1")); v == "" {
		t.Fatal("w1 not recorded")
	}
	if v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanCustomTypes, "w2")); v != "" {
		t.Fatal("w2 recorded by w1's 402")
	}
	if n := len(readmodelHidden(t, st, "w2")); n != 0 {
		t.Fatalf("w2 hidden: %d", n)
	}
}

func readmodelHidden(t *testing.T, st *store.Store, ws string) []string {
	t.Helper()
	v, _ := st.SyncState(context.Background(), readmodel.PlanKey(readmodel.PlanCustomTypes, ws))
	if v == "" {
		return nil
	}
	return []string{v}
}

// Two pushes at once send the change once.
func TestAttackConcurrentPush(t *testing.T) {
	f, s, st, _ := setupAtk(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "x", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var wg stdsync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Push(ctx) }()
	}
	wg.Wait()
	if n := f.count("PUT /tasks/t9"); n != 1 {
		t.Fatalf("%d PUTs", n)
	}
}

// The answer is a task read without the optional fields (Partial): it is not judged as
// "ignored" and the change completes.
func TestAttackSilentTypeDropIsCaught(t *testing.T) {
	// Asana answers the PUT with a task that is no longer custom (e.g. the type was deleted).
	f, s, st, _ := setupAtk(t)
	ctx := context.Background()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t9", Name: "x", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateTaskLocal(ctx, "t9", map[string]any{"resource_subtype": "custom", "custom_type": "ct1"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	f.fail("PUT /tasks/t9", 404, 1) // the type or task is gone
	res, _ := s.Push(ctx)
	ops, _ := st.ListOps(ctx)
	if res.Failed == 0 && len(ops) == 1 && ops[0].State != "failed" {
		// 404 on a PUT: acceptable only if it is not retried forever; one more push settles it.
		_, _ = s.Push(ctx)
		if f.count("PUT /tasks/t9") > 3 {
			t.Fatalf("kept sending: %v", f.log)
		}
	}
}
