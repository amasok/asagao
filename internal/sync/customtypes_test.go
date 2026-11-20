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
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

// Custom task types (#59) against a fake Asana: the list of a project's types, the 402
// of a plan without them, and PUT /tasks with a type.

type typesServer struct {
	t        *testing.T
	mu       stdsync.Mutex
	typeGets []string // query of each GET /custom_types
	puts     []map[string]any
	refuse   bool // 402 for GET /custom_types and for a PUT with a custom type
	reject   bool // 400 for a PUT with a custom type (the API does not take it)
	ignore   bool // a PUT answers 200 without applying the custom type
	failRest bool // a PUT without a custom type answers 503
	// task is t1 as Asana has it.
	subtype, typ, status string
	name                 string // "" is "A"
	completed            bool
}

// taskJSON is t1 as Asana answers r. Like Asana, a custom type or status asked for by its
// name only (custom_type.name) comes without its gid: the gid comes with custom_type or
// custom_type.gid (found against the real Asana, #59).
func (f *typesServer) taskJSON(r *http.Request) string {
	ct, cs := "null", "null"
	if f.subtype == "custom" {
		ct = askedRef(r, "custom_type", f.typ, "バグ")
		cs = askedRef(r, "custom_type_status_option", f.status, "対応中")
	}
	name := f.name
	if name == "" {
		name = "A"
	}
	return fmt.Sprintf(`{"gid":"t1","name":%q,"resource_subtype":%q,"custom_type":%s,"custom_type_status_option":%s,"due_on":"2026-12-31","completed":%t,
		"workspace":{"gid":"w1"},"memberships":[{"project":{"gid":"p1"}}],"modified_at":"2026-11-13T00:00:00Z"}`, name, f.subtype, ct, cs, f.completed)
}

// askedRef is a reference field (custom_type) as Asana answers it for the opt_fields of r.
func askedRef(r *http.Request, field, gid, name string) string {
	asked := map[string]bool{}
	for _, o := range strings.Split(r.URL.Query().Get("opt_fields"), ",") {
		asked[o] = true
	}
	ref := map[string]string{}
	if asked[field] || asked[field+".gid"] {
		ref["gid"] = gid
	}
	if asked[field] || asked[field+".name"] {
		ref["name"] = name
	}
	if len(ref) == 0 {
		return "null"
	}
	b, _ := json.Marshal(ref)
	return string(b)
}

func (f *typesServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/custom_types":
		f.typeGets = append(f.typeGets, r.URL.RawQuery)
		if f.refuse {
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"errors":[{"message":"payment required"}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"gid":"ct1","name":"バグ","status_options":[
			{"gid":"cs-doing","name":"対応中","completion_state":"Incomplete","enabled":true,"color":"blue"},
			{"gid":"cs-done","name":"完了","completion_state":"Complete","enabled":true}]}]}`)
	case r.URL.Path == "/tasks/t1" && r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(b, &body)
		f.puts = append(f.puts, body.Data)
		_, custom := body.Data["custom_type"]
		if f.failRest && !custom {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"errors":[{"message":"later"}]}`)
			return
		}
		if f.refuse && custom {
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"errors":[{"message":"payment required"}]}`)
			return
		}
		if f.reject && custom {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"errors":[{"message":"custom_type: Unknown field"}]}`)
			return
		}
		if v, ok := body.Data["name"].(string); ok {
			f.name = v
		}
		if f.ignore {
			fmt.Fprintf(w, `{"data":%s}`, f.taskJSON(r))
			return
		}
		if v, ok := body.Data["resource_subtype"].(string); ok {
			f.subtype = v
		}
		if v, ok := body.Data["custom_type"].(string); ok {
			f.typ = v
		}
		if v, ok := body.Data["custom_type_status_option"].(string); ok {
			f.status = v
			// As the real Asana does (checked): a task put in a status whose
			// completion_state is Complete is completed, and reopened by another.
			f.completed = v == "cs-done"
		}
		if v, ok := body.Data["completed"].(bool); ok {
			f.completed = v
		}
		fmt.Fprintf(w, `{"data":%s}`, f.taskJSON(r))
	case strings.HasPrefix(r.URL.Path, "/tasks/t1/") || r.URL.Path == "/attachments": // subtasks, stories, attachments
		fmt.Fprint(w, `{"data":[]}`)
	case r.URL.Path == "/tasks/t1":
		fmt.Fprintf(w, `{"data":%s}`, f.taskJSON(r))
	default:
		http.NotFound(w, r)
	}
}

func setupTypes(t *testing.T) (*typesServer, *Syncer, *store.Store, *time.Time) {
	t.Helper()
	f := &typesServer{t: t, subtype: "default_task"}
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
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A", Workspace: &asana.Ref{GID: "w1"},
		Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}}}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	s := &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}, now: func() time.Time { return clock }}
	return f, s, st, &clock
}

func TestSyncCustomTypesOfProject(t *testing.T) {
	f, s, st, _ := setupTypes(t)
	ctx := context.Background()
	if err := s.Puller().syncCustomTypes(ctx, "p1", "w1", false); err != nil {
		t.Fatal(err)
	}
	if len(f.typeGets) != 1 || !strings.Contains(f.typeGets[0], "project=p1") || !strings.Contains(f.typeGets[0], "status_options.completion_state") {
		t.Fatalf("GET /custom_types %v", f.typeGets)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.CustomTypes) != 1 || d.CustomTypes[0].Name != "バグ" || len(d.CustomTypes[0].Statuses) != 2 || !d.CustomTypes[0].Statuses[1].Complete {
		t.Fatalf("types %+v", d.CustomTypes)
	}
	// Not again until a full sync.
	if err := s.Puller().syncCustomTypes(ctx, "p1", "w1", false); err != nil || len(f.typeGets) != 1 {
		t.Fatalf("fetched again: %v %v", f.typeGets, err)
	}
	if err := s.Puller().syncCustomTypes(ctx, "p1", "w1", true); err != nil || len(f.typeGets) != 2 {
		t.Fatalf("full sync did not fetch: %v %v", f.typeGets, err)
	}
}

// A 402 records the workspace's plan, is not asked again for a while (not even by a full
// sync, nor for another project of it), and is asked again after that.
func TestSyncCustomTypesPaymentRequired(t *testing.T) {
	f, s, st, clock := setupTypes(t)
	ctx := context.Background()
	f.refuse = true
	if err := s.Puller().syncCustomTypes(ctx, "p1", "w1", true); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.CustomTypes, "w1"); !ok {
		t.Fatal("402 not recorded")
	}
	for _, p := range []string{"p1", "p2"} {
		if err := s.Puller().syncCustomTypes(ctx, p, "w1", true); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.typeGets) != 1 {
		t.Fatalf("asked again during the 402: %v", f.typeGets)
	}
	// Another workspace is not hidden by it.
	if err := s.Puller().syncCustomTypes(ctx, "p3", "w2", true); err != nil || len(f.typeGets) != 2 {
		t.Fatalf("another workspace: %v %v", f.typeGets, err)
	}
	*clock = clock.Add(plan.RecheckAfter + time.Minute)
	f.refuse = false
	if err := s.Puller().syncCustomTypes(ctx, "p1", "w1", false); err != nil || len(f.typeGets) != 3 {
		t.Fatalf("not asked again after the recheck: %v %v", f.typeGets, err)
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.CustomTypes, "w1"); ok {
		t.Fatal("402 not forgotten")
	}
}
