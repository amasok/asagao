package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	stdsync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// fieldServer is a fake Asana with one task t1 that has an enum field cf1 (options o1, o2),
// unless hasField is false.
type fieldServer struct {
	mu       stdsync.Mutex
	value    string // enum option gid, "" = empty
	hasField bool
	puts     []string
	settings int // answer of custom_field_settings: 0 = the field, else this status
}

func (f *fieldServer) taskJSON() string {
	fields := "[]"
	if f.hasField {
		ev, dv := "null", "null"
		if f.value != "" {
			ev = fmt.Sprintf(`{"gid":%q,"name":%q}`, f.value, map[string]string{"o1": "高", "o2": "中"}[f.value])
			dv = fmt.Sprintf("%q", map[string]string{"o1": "高", "o2": "中"}[f.value])
		}
		fields = fmt.Sprintf(`[{"gid":"cf1","name":"優先度","resource_subtype":"enum","enum_value":%s,"display_value":%s}]`, ev, dv)
	}
	return fmt.Sprintf(`{"gid":"t1","name":"A","modified_at":"2026-11-13T00:00:00Z","custom_fields":%s}`, fields)
}

func (f *fieldServer) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/users/me":
		fmt.Fprint(rw, `{"data":{"gid":"me","name":"Me"}}`)
	case r.URL.Path == "/projects/p1/custom_field_settings":
		if f.settings != 0 {
			rw.WriteHeader(f.settings)
			fmt.Fprint(rw, `{"errors":[{"message":"premium only"}]}`)
			return
		}
		fmt.Fprint(rw, `{"data":[{"gid":"s1","is_important":true,"custom_field":{"gid":"cf1","name":"優先度","resource_subtype":"enum",
			"enum_options":[{"gid":"o1","name":"高","enabled":true},{"gid":"o2","name":"中","enabled":true}]}}]}`)
	case r.URL.Path == "/tasks/t1" && r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.puts = append(f.puts, string(b))
		var body struct {
			Data struct {
				CustomFields map[string]*string `json:"custom_fields"`
			} `json:"data"`
		}
		_ = json.Unmarshal(b, &body)
		if v, ok := body.Data.CustomFields["cf1"]; ok {
			f.value = ""
			if v != nil {
				f.value = *v
			}
		}
		fmt.Fprintf(rw, `{"data":%s}`, f.taskJSON())
	case r.URL.Path == "/tasks/t1":
		fmt.Fprintf(rw, `{"data":%s}`, f.taskJSON())
	default:
		rw.WriteHeader(http.StatusNotFound)
		fmt.Fprint(rw, `{"errors":[{"message":"not found"}]}`)
	}
}

func setupFieldPush(t *testing.T) (*fieldServer, *Syncer, *store.Store) {
	t.Helper()
	fs := &fieldServer{value: "o1", hasField: true}
	srv := httptest.NewServer(fs)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL, api.MaxRetries = srv.URL, 0
	s := &Syncer{API: api, Store: st, Cfg: Config{CompletedDays: 14}}
	if err := st.SetMeGID(ctx, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.syncCustomFields(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	task, err := api.Task(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	task.Memberships = []asana.Membership{{Project: &asana.Ref{GID: "p1"}}}
	if err := st.UpsertTasks(ctx, []asana.Task{*task}); err != nil {
		t.Fatal(err)
	}
	return fs, s, st
}

func TestPushCustomField(t *testing.T) {
	fs, s, st := setupFieldPush(t)
	ctx := context.Background()
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if fs.value != "o2" || len(fs.puts) != 1 || fs.puts[0] != `{"data":{"custom_fields":{"cf1":"o2"}}}` {
		t.Fatalf("server value %q, puts %v", fs.value, fs.puts)
	}
	// clearing sends null
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil || fs.value != "" {
		t.Fatalf("clear: value %q, %v", fs.value, err)
	}
}

func TestPushCustomFieldConflict(t *testing.T) {
	fs, s, st := setupFieldPush(t)
	ctx := context.Background()
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now); err != nil {
		t.Fatal(err)
	}
	fs.value = "" // someone cleared it in Asana meanwhile
	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 || len(fs.puts) != 0 {
		t.Fatalf("push = %+v, %v, puts %v", res, err, fs.puts)
	}
	ops, _ := st.ListOps(ctx)
	if lines := store.ConflictLines(ops[0]); len(lines) != 1 || lines[0] != "優先度: こちら=中 / Asana=（空）" {
		t.Fatalf("conflict lines = %q", lines)
	}
}

func TestPushCustomFieldGoneIsDropped(t *testing.T) {
	fs, s, st := setupFieldPush(t)
	ctx := context.Background()
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now); err != nil {
		t.Fatal(err)
	}
	fs.hasField = false // removed from the project
	res, err := s.Push(ctx)
	if err != nil || len(res.Discarded) != 1 || len(fs.puts) != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if p, c, f := pendingCount(t, st); p+c+f != 0 {
		t.Fatalf("outbox = %d %d %d", p, c, f)
	}
}

func TestCustomFieldsUnavailableIsNotAnError(t *testing.T) {
	fs, s, st := setupFieldPush(t)
	ctx := context.Background()
	fs.settings = http.StatusPaymentRequired
	if err := s.syncCustomFields(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.CustomFields) != 1 { // the task's own value is still shown
		t.Fatalf("fields = %+v", d.CustomFields)
	}
}

// An expired token (401) keeps the cached fields and fetches them again next time.
func TestCustomFieldsKeptOnUnauthorized(t *testing.T) {
	fs, s, st := setupFieldPush(t)
	ctx := context.Background()
	if err := st.SetProjectCustomFieldsFetchedAt(ctx, "p1", time.Time{}); err != nil {
		t.Fatal(err)
	}
	fs.settings = http.StatusUnauthorized
	if err := s.syncCustomFields(ctx, "p1", false); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.ProjectCustomFieldsFetchedAt(ctx, "p1"); v != "" {
		t.Fatalf("a failed fetch must not be recorded as done: %q", v)
	}
	d, _ := st.GetTask(ctx, "t1")
	if len(d.CustomFields) != 1 || len(d.CustomFields[0].Options) != 2 {
		t.Fatalf("fields = %+v", d.CustomFields)
	}
}
