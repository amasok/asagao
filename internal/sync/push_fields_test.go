package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	stdsync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// optionServer is a fake Asana for #37: workspace w1 with the fields cf1 (enum: o1 高,
// o2 中), cf2 (multi_enum: m1 A, m2 B) and cf3 (text); project p1 has cf1 and cf2; tasks
// t1 and t2 are in p1. Values set with PUT /tasks are kept.
type optionServer struct {
	t       *testing.T
	mu      stdsync.Mutex
	fields  map[string]*asana.CustomField
	order   []string
	project []string                  // the fields of p1
	values  map[string]map[string]any // task -> field -> value as sent
	calls   []string
	next    int
	// status, if set, answers the matching request ("POST /custom_fields/cf1/enum_options")
	// with that status. 500 on a create: the option is created and the answer lost.
	status map[string]int
}

func (f *optionServer) fieldJSON(g string, withValue string) map[string]any {
	d := f.fields[g]
	m := map[string]any{"gid": d.GID, "name": d.Name, "resource_subtype": d.ResourceSubtype}
	if withValue == "" {
		if d.EnumOptions != nil {
			m["enum_options"] = d.EnumOptions
		}
		return m
	}
	v := f.values[withValue][g]
	name := func(o string) string {
		for _, e := range d.EnumOptions {
			if e.GID == o {
				return e.Name
			}
		}
		return o
	}
	switch d.ResourceSubtype {
	case "enum":
		if s, ok := v.(string); ok {
			m["enum_value"] = map[string]any{"gid": s, "name": name(s)}
			m["display_value"] = name(s)
		} else {
			m["enum_value"] = nil
		}
	case "multi_enum":
		var vs []map[string]any
		var names []string
		for _, e := range optionGIDs(v) {
			vs = append(vs, map[string]any{"gid": e, "name": name(e)})
			names = append(names, name(e))
		}
		m["multi_enum_values"] = vs
		m["display_value"] = strings.Join(names, ", ")
	case "text":
		m["text_value"] = v
	}
	return m
}

func (f *optionServer) taskJSON(gid string) string {
	var cfs []map[string]any
	for _, g := range f.project {
		cfs = append(cfs, f.fieldJSON(g, gid))
	}
	js, _ := json.Marshal(map[string]any{"gid": gid, "name": gid, "modified_at": "2026-11-13T00:00:00Z", "custom_fields": cfs,
		"memberships": []any{map[string]any{"project": map[string]any{"gid": "p1"}}}})
	return string(js)
}

func (f *optionServer) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	var body struct{ Data map[string]any }
	_ = json.Unmarshal(b, &body)
	call := r.Method + " " + r.URL.Path
	f.calls = append(f.calls, strings.TrimSpace(call+" "+string(b)))
	status := f.status[call]
	fail := func(code int, msg string) {
		rw.WriteHeader(code)
		fmt.Fprintf(rw, `{"errors":[{"message":%q}]}`, msg)
	}
	if status != 0 && status != 500 {
		fail(status, "refused")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	str := func(k string) string { v, _ := body.Data[k].(string); return v }
	switch {
	case r.URL.Path == "/users/me":
		fmt.Fprint(rw, `{"data":{"gid":"me","name":"Me"}}`)
	case len(parts) == 2 && parts[0] == "custom_fields" && r.Method == http.MethodGet:
		if f.fields[parts[1]] == nil {
			fail(404, "not found")
			return
		}
		js, _ := json.Marshal(f.fieldJSON(parts[1], ""))
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case len(parts) == 3 && parts[0] == "custom_fields" && parts[2] == "enum_options":
		d := f.fields[parts[1]]
		if d == nil {
			fail(404, "not found")
			return
		}
		f.next++
		yes := true
		o := asana.EnumOption{GID: fmt.Sprintf("new%d", f.next), Name: str("name"), Color: "blue", Enabled: &yes}
		d.EnumOptions = append(d.EnumOptions, o)
		if status == 500 {
			fail(500, "lost")
			return
		}
		js, _ := json.Marshal(o)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/workspaces/w1/custom_fields":
		var out []map[string]any
		for _, g := range f.order {
			out = append(out, f.fieldJSON(g, ""))
		}
		js, _ := json.Marshal(out)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/projects/p1/custom_field_settings":
		var out []map[string]any
		for i, g := range f.project {
			out = append(out, map[string]any{"gid": fmt.Sprintf("s%d", i), "custom_field": f.fieldJSON(g, "")})
		}
		js, _ := json.Marshal(out)
		fmt.Fprintf(rw, `{"data":%s}`, js)
	case r.URL.Path == "/projects/p1/addCustomFieldSetting":
		if slices.Contains(f.project, str("custom_field")) {
			fail(400, "already on the project")
			return
		}
		f.project = append(f.project, str("custom_field"))
		fmt.Fprint(rw, `{"data":{}}`)
	case r.URL.Path == "/projects/p1/removeCustomFieldSetting":
		i := slices.Index(f.project, str("custom_field"))
		if i < 0 {
			fail(400, "not on the project")
			return
		}
		f.project = slices.Delete(f.project, i, i+1)
		fmt.Fprint(rw, `{"data":{}}`)
	case len(parts) == 2 && parts[0] == "tasks":
		if r.Method == http.MethodPut {
			cfs, _ := body.Data["custom_fields"].(map[string]any)
			for k, v := range cfs {
				if !slices.Contains(f.project, k) {
					fail(400, "custom field not on the task")
					return
				}
				d := f.fields[k]
				for _, o := range optionGIDs(v) {
					if d.ResourceSubtype != "text" && !slices.ContainsFunc(d.EnumOptions, func(e asana.EnumOption) bool { return e.GID == o }) {
						fail(400, "unknown enum option "+o)
						return
					}
				}
				if f.values[parts[1]] == nil {
					f.values[parts[1]] = map[string]any{}
				}
				f.values[parts[1]][k] = v
			}
		}
		fmt.Fprintf(rw, `{"data":%s}`, f.taskJSON(parts[1]))
	default:
		f.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		fail(http.StatusTeapot, "unexpected")
	}
}

func (f *optionServer) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *optionServer) value(task, field string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[task][field]
}

func setupOptions(t *testing.T) (*optionServer, *Syncer, *store.Store) {
	t.Helper()
	yes := true
	f := &optionServer{t: t, status: map[string]int{}, values: map[string]map[string]any{},
		fields: map[string]*asana.CustomField{
			"cf1": {GID: "cf1", Name: "優先度", ResourceSubtype: "enum",
				EnumOptions: []asana.EnumOption{{GID: "o1", Name: "高", Enabled: &yes}, {GID: "o2", Name: "中", Enabled: &yes}}},
			"cf2": {GID: "cf2", Name: "ラベル", ResourceSubtype: "multi_enum",
				EnumOptions: []asana.EnumOption{{GID: "m1", Name: "A", Enabled: &yes}, {GID: "m2", Name: "B", Enabled: &yes}}},
			"cf3": {GID: "cf3", Name: "メモ", ResourceSubtype: "text"},
		},
		order:   []string{"cf1", "cf2", "cf3"},
		project: []string{"cf1", "cf2"},
	}
	srv := httptest.NewServer(f)
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
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.SetSyncState(ctx, store.KeyMeGID, "me"))
	must(st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"))
	must(s.syncCustomFields(ctx, "p1", true))
	must(s.syncWorkspaceCustomFields(ctx, "w1", true))
	for _, g := range []string{"t1", "t2"} {
		task, err := api.Task(ctx, g)
		must(err)
		must(st.UpsertTasks(ctx, []asana.Task{*task}))
	}
	f.calls = nil
	return f, s, st
}

func fieldOf(t *testing.T, st *store.Store, task, field string) readmodel.CustomFieldView {
	t.Helper()
	d, err := st.GetTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.CustomFields {
		if f.GID == field {
			return f
		}
	}
	t.Fatalf("task %s has no field %s: %+v", task, field, d.CustomFields)
	return readmodel.CustomFieldView{}
}

// A value with an option added offline is sent after the option is created, with its gid.
func TestPushNewOptionThenValue(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, created, err := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if err != nil || !created || !store.IsLocalGID(local) {
		t.Fatalf("add = %q %v %v", local, created, err)
	}
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now); err != nil {
		t.Fatal(err)
	}
	if v := fieldOf(t, st, "t1", "cf1"); v.Display != "低" || v.Value != local {
		t.Fatalf("local value = %+v", v)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v; calls %v", res, err, f.calls)
	}
	if got := f.value("t1", "cf1"); got != "new1" {
		t.Fatalf("Asana's value = %v; calls %v", got, f.calls)
	}
	if f.count("POST /custom_fields/cf1/enum_options") != 1 {
		t.Fatalf("calls %v", f.calls)
	}
	v := fieldOf(t, st, "t1", "cf1")
	if v.Value != "new1" || v.Display != "低" {
		t.Fatalf("stored value = %+v", v)
	}
	if !slices.ContainsFunc(v.Options, func(o readmodel.EnumOptionView) bool { return o.GID == "new1" && o.Name == "低" }) ||
		slices.ContainsFunc(v.Options, func(o readmodel.EnumOptionView) bool { return o.GID == local }) {
		t.Fatalf("options = %+v", v.Options)
	}
	if real, _ := st.RealEnumOptionGID(ctx, local); real != "new1" {
		t.Fatalf("result = %q", real)
	}
}

// A multi_enum value with an option added offline keeps the other options.
func TestPushNewOptionMultiEnum(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, err := st.AddEnumOptionLocal(ctx, "cf2", "C", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf2", []string{"m2", local}, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v, %v; calls %v", res, err, f.calls)
	}
	got := optionGIDs(f.value("t1", "cf2"))
	slices.Sort(got)
	if !slices.Equal(got, []string{"m2", "new1"}) {
		t.Fatalf("Asana's value = %v", got)
	}
	if v := fieldOf(t, st, "t1", "cf2"); fmt.Sprint(v.Value) != "[m2 new1]" || v.Display != "B, C" {
		t.Fatalf("stored = %+v", v)
	}
}

// An option Asana already has with the name (added meanwhile) is taken, not created twice.
func TestPushNewOptionTakesExisting(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now); err != nil {
		t.Fatal(err)
	}
	yes := true
	f.fields["cf1"].EnumOptions = append(f.fields["cf1"].EnumOptions, asana.EnumOption{GID: "o9", Name: "低", Enabled: &yes})
	if res, err := s.Push(ctx); err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if f.count("POST /custom_fields/cf1/enum_options") != 0 || f.value("t1", "cf1") != "o9" {
		t.Fatalf("calls %v", f.calls)
	}
}

// The answer to the create is lost (5xx after creating it): the check finds it, and it is
// not created again.
func TestPushNewOptionLostAnswer(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now); err != nil {
		t.Fatal(err)
	}
	f.status["POST /custom_fields/cf1/enum_options"] = 500
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	delete(f.status, "POST /custom_fields/cf1/enum_options")
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count("POST /custom_fields/cf1/enum_options"); n != 1 {
		t.Fatalf("created %d times; calls %v", n, f.calls)
	}
	if f.value("t1", "cf1") != "new1" {
		t.Fatalf("value = %v; calls %v", f.value("t1", "cf1"), f.calls)
	}
	if n, _, _, _ := st.OutboxCounts(ctx); n != 0 {
		t.Fatalf("left %d", n)
	}
}

// Asana refuses the option (403): the value waits (never sent with the local gid, also
// not in a batch with another task's change), adding options to that field is not offered
// any more, and discarding the create drops the value and puts back the old one.
func TestPushNewOptionForbidden(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetCustomFieldLocal(ctx, "t2", "cf1", "o2", now); err != nil {
		t.Fatal(err)
	}
	f.status["POST /custom_fields/cf1/enum_options"] = 403
	res, err := s.Push(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed != 1 || res.Sent != 1 || f.value("t2", "cf1") != "o2" || f.value("t1", "cf1") != nil {
		t.Fatalf("push = %+v; calls %v", res, f.calls)
	}
	for _, c := range f.calls {
		if strings.Contains(c, local) {
			t.Fatalf("sent the local gid: %s", c)
		}
	}
	if v := fieldOf(t, st, "t1", "cf1"); v.CanAddOption {
		t.Fatalf("still offered: %+v", v)
	}
	if v := fieldOf(t, st, "t1", "cf2"); !v.CanAddOption {
		t.Fatalf("other field not offered: %+v", v)
	}
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var create int64
	for _, op := range ops {
		if op.Kind == store.OpCreateEnumOption {
			create = op.ID
			if !strings.Contains(op.LastError, "403") {
				t.Fatalf("error = %q", op.LastError)
			}
		}
	}
	if err := st.DiscardOp(ctx, create); err != nil {
		t.Fatal(err)
	}
	if n, _, failed, _ := st.OutboxCounts(ctx); n+failed != 0 {
		t.Fatalf("left %d pending %d failed", n, failed)
	}
	v := fieldOf(t, st, "t1", "cf1")
	if v.Value != nil || slices.ContainsFunc(v.Options, func(o readmodel.EnumOptionView) bool { return o.GID == local }) {
		t.Fatalf("after discard = %+v", v)
	}
}

// 402: the plan has no custom fields edits; it is not offered any more.
func TestPushNewOptionPaymentRequired(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	if _, _, err := st.AddEnumOptionLocal(ctx, "cf1", "低", now); err != nil {
		t.Fatal(err)
	}
	f.status["POST /custom_fields/cf1/enum_options"] = 402
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanCustomFields); v == "" {
		t.Fatal("plan not recorded")
	}
	if v := fieldOf(t, st, "t1", "cf2"); v.CanAddOption {
		t.Fatal("still offered")
	}
}

// A pull keeps an option added offline (and a value using it) until it is sent.
func TestPullKeepsLocalOption(t *testing.T) {
	_, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now); err != nil {
		t.Fatal(err)
	}
	if err := s.syncCustomFields(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	if err := s.syncWorkspaceCustomFields(ctx, "w1", true); err != nil {
		t.Fatal(err)
	}
	task, err := s.API.Task(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{*task}); err != nil {
		t.Fatal(err)
	}
	v := fieldOf(t, st, "t1", "cf1")
	if v.Value != local || v.Display != "低" || !slices.ContainsFunc(v.Options, func(o readmodel.EnumOptionView) bool { return o.GID == local }) {
		t.Fatalf("after pull = %+v", v)
	}
}

// A field added to the project is sent before a value of it; the project's fields are
// fetched again.
func TestPushAddFieldSettingThenValue(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf3", true, now); err != nil {
		t.Fatal(err)
	}
	v := fieldOf(t, st, "t1", "cf3") // offered on the project's tasks at once
	if !v.Editable {
		t.Fatalf("field = %+v", v)
	}
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf3", "メモです", now); err != nil {
		t.Fatal(err)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 2 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v; calls %v", res, err, f.calls)
	}
	if f.value("t1", "cf3") != "メモです" {
		t.Fatalf("calls %v", f.calls)
	}
	fields, err := st.ProjectFields(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	var attached []string
	for _, pf := range fields {
		if pf.Attached {
			attached = append(attached, pf.GID)
		}
		if pf.Pending {
			t.Fatalf("still pending: %+v", pf)
		}
	}
	if !slices.Equal(attached, []string{"cf1", "cf2", "cf3"}) {
		t.Fatalf("attached = %v", attached)
	}
}

// The value of a field whose adding failed waits; it is not sent (Asana would refuse it).
func TestValueWaitsForFailedFieldSetting(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf3", true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf3", "メモ", now); err != nil {
		t.Fatal(err)
	}
	f.status["POST /projects/p1/addCustomFieldSetting"] = 403
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 || res.Sent != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
	if f.count("PUT /tasks/t1") != 0 {
		t.Fatalf("calls %v", f.calls)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyProjectFieldsLocked+"p1"); v == "" {
		t.Fatal("403 not recorded")
	}
}

// Adding a field the project already has in Asana (someone else added it) is done.
func TestPushAddFieldSettingAlreadyThere(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf3", true, now); err != nil {
		t.Fatal(err)
	}
	f.project = append(f.project, "cf3")
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || res.Failed != 0 {
		t.Fatalf("push = %+v, %v", res, err)
	}
}

// Removing a field from the project.
func TestPushRemoveFieldSetting(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf2", false, now); err != nil {
		t.Fatal(err)
	}
	// a pull before it is sent keeps it removed
	if err := s.syncCustomFields(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	fields, _ := st.ProjectFields(ctx, "p1")
	for _, pf := range fields {
		if pf.GID == "cf2" && (pf.Attached || !pf.Pending) {
			t.Fatalf("after pull = %+v", pf)
		}
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 || !slices.Equal(f.project, []string{"cf1"}) {
		t.Fatalf("push = %+v, %v; project %v", res, err, f.project)
	}
}

// The workspace's fields: stored; 402 records that the plan lacks custom fields.
func TestSyncWorkspaceCustomFields(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	fields, err := st.ProjectFields(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, pf := range fields {
		got = append(got, fmt.Sprintf("%s:%v", pf.Name, pf.Attached))
	}
	if strings.Join(got, ",") != "優先度:true,ラベル:true,メモ:false" {
		t.Fatalf("fields = %v", got)
	}
	f.status["GET /workspaces/w1/custom_fields"] = 402
	if err := s.syncWorkspaceCustomFields(ctx, "w1", true); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanCustomFields); v == "" {
		t.Fatal("plan not recorded")
	}
	fields, _ = st.ProjectFields(ctx, "p1")
	if len(fields) != 2 {
		t.Fatalf("fields = %+v", fields)
	}
	// another error keeps what is cached, and is tried again
	f.status["GET /workspaces/w1/custom_fields"] = 500
	if err := s.syncWorkspaceCustomFields(ctx, "w1", true); err != nil {
		t.Fatal(err)
	}
}
