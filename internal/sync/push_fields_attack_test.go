package sync

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/store"
)

func noLocalGIDSent(t *testing.T, f *optionServer) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.Contains(c, store.LocalPrefix) {
			t.Errorf("a local gid went out: %s", c)
		}
	}
}

// Values of several tasks that use a still-queued option are not put in a /batch with the
// local gid, even though the create fails (4xx/5xx) and stays failed.
func TestAttackValuesWaitForFailedCreateInBatch(t *testing.T) {
	for _, code := range []int{400, 403, 429} {
		f, s, st := setupOptions(t)
		ctx := context.Background()
		local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
		for _, task := range []string{"t1", "t2"} {
			if _, err := st.SetCustomFieldLocal(ctx, task, "cf1", local, now); err != nil {
				t.Fatal(err)
			}
		}
		f.status["POST /custom_fields/cf1/enum_options"] = code
		for i := 0; i < 3; i++ { // sync again and again
			if _, err := s.Push(ctx); err != nil && code != 429 {
				t.Fatalf("%d: %v", code, err)
			}
		}
		noLocalGIDSent(t, f)
		if f.value("t1", "cf1") != nil || f.value("t2", "cf1") != nil {
			t.Fatalf("%d: a value went out: %v %v", code, f.value("t1", "cf1"), f.value("t2", "cf1"))
		}
		noLocalGIDSent(t, f)
	}
}

// A task created offline whose values use a queued option: created with the real gid, or
// not at all before the option exists.
func TestAttackCreateTaskWithLocalOption(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	tg, err := st.CreateTaskLocal(ctx, store.NewTask{WorkspaceGID: "w1", ProjectGID: "p1", Name: "new"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetCustomFieldLocal(ctx, tg, "cf1", local, now); err != nil {
		t.Skipf("custom field on a local task is not supported here: %v", err)
	}
	f.status["POST /custom_fields/cf1/enum_options"] = 403
	for i := 0; i < 3; i++ {
		_, _ = s.Push(ctx)
	}
	noLocalGIDSent(t, f)
	if f.count("POST /tasks") != 0 {
		t.Fatalf("the task was created before its option: %v", f.calls)
	}
}

// Asana has the same name twice (one disabled, one enabled): the enabled one is taken.
func TestAttackSameNameDisabledAndEnabled(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	no, yes := false, true
	f.fields["cf1"].EnumOptions = append(f.fields["cf1"].EnumOptions,
		asana.EnumOption{GID: "d1", Name: "低", Enabled: &no}, asana.EnumOption{GID: "e1", Name: "低", Enabled: &yes},
		asana.EnumOption{GID: "e2", Name: "低", Enabled: &yes})
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.value("t1", "cf1"); got != "e1" {
		t.Fatalf("value = %v; calls %v", got, f.calls)
	}
	if f.count("POST /custom_fields/cf1/enum_options") != 0 {
		t.Fatal("created a duplicate")
	}
}

// Only a disabled option has the name: a new one is created (the disabled one is not reused).
func TestAttackOnlyDisabledNamed(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	no := false
	f.fields["cf1"].EnumOptions = append(f.fields["cf1"].EnumOptions, asana.EnumOption{GID: "d1", Name: "低", Enabled: &no})
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.value("t1", "cf1"); got == "d1" || got == nil {
		t.Fatalf("value = %v", got)
	}
}

// Two different local options with the same name in two syncs, lost answer for both: the
// two entries must not take the same Asana option.
func TestAttackTwoEntriesDoNotTakeSameOption(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	a, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	b, _, _ := st.AddEnumOptionLocal(ctx, "cf2", "低", now) // another field, same name
	_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", a, now)
	_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf2", []string{b}, now)
	if res, err := s.Push(ctx); err != nil || res.Failed != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	if f.value("t1", "cf1") == nil || f.value("t1", "cf2") == nil {
		t.Fatalf("calls %v", f.calls)
	}
	noLocalGIDSent(t, f)
}

// Adding the same name again (any case, spaces, control characters) does not queue a
// second create; the name is sanitised.
func TestAttackAddOptionNames(t *testing.T) {
	_, _, st := setupOptions(t)
	ctx := context.Background()
	g1, c1, _ := st.AddEnumOptionLocal(ctx, "cf1", "  低  ", now)
	g2, c2, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	g3, c3, _ := st.AddEnumOptionLocal(ctx, "cf1", "高", now)
	if !c1 || c2 || c3 || g1 != g2 || g3 != "o1" {
		t.Fatalf("%v %v %v / %v %v %v", g1, g2, g3, c1, c2, c3)
	}
	for _, bad := range []string{"", "   ", "\x1b", "\n\t"} {
		if _, created, err := st.AddEnumOptionLocal(ctx, "cf1", bad, now); err == nil && created {
			t.Errorf("accepted %q", bad)
		}
	}
	g, _, err := st.AddEnumOptionLocal(ctx, "cf1", "赤\x1b]0;x\x07い", now)
	if err != nil {
		t.Fatal(err)
	}
	v := fieldOf(t, st, "t1", "cf1")
	for _, o := range v.Options {
		if o.GID == g && strings.ContainsAny(o.Name, "\x1b\x07") {
			t.Fatalf("name = %q", o.Name)
		}
	}
	if _, _, err := st.AddEnumOptionLocal(ctx, "cf3", "x", now); err == nil {
		t.Fatal("text field accepted an option")
	}
	if _, _, err := st.AddEnumOptionLocal(ctx, "nope", "x", now); err == nil {
		t.Fatal("unknown field accepted an option")
	}
}

// Undo of a create: the enum goes back to the base value, a multi_enum keeps the others.
func TestAttackUndoCreateOption(t *testing.T) {
	_, s, st := setupOptions(t)
	ctx := context.Background()
	if _, err := st.SetCustomFieldLocal(ctx, "t1", "cf1", "o2", now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil { // Asana has o2 now
		t.Fatal(err)
	}
	loc1, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	loc2, _, _ := st.AddEnumOptionLocal(ctx, "cf2", "C", now)
	_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", loc1, now)
	_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf2", []string{"m1", loc2}, now)
	_, _ = st.SetCustomFieldLocal(ctx, "t2", "cf2", []string{loc2}, now)
	ops, _ := st.ListOps(ctx)
	for _, op := range ops {
		if op.Kind == store.OpCreateEnumOption {
			if err := st.DiscardOp(ctx, op.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if v := fieldOf(t, st, "t1", "cf1"); v.Value != "o2" || v.Display != "中" {
		t.Fatalf("cf1 = %+v", v)
	}
	if v := fieldOf(t, st, "t1", "cf2"); strings.Contains(v.Display, "C") || !strings.Contains(v.Display, "A") || len(v.Options) != 2 {
		t.Fatalf("cf2 = %+v", v)
	}
	if v := fieldOf(t, st, "t2", "cf2"); v.Value != nil && len(v.Value.([]string)) != 0 {
		t.Fatalf("t2 cf2 = %+v", v)
	}
	rest, _ := st.ListOps(ctx)
	for _, op := range rest {
		if strings.Contains(op.LastError, store.LocalPrefix) || strings.Contains(strings.Join(mapStr(op.Payload), " "), store.LocalPrefix) {
			t.Fatalf("op still names a discarded option: %+v", op)
		}
	}
}

func mapStr(m map[string]any) []string {
	var out []string
	for k, v := range m {
		out = append(out, k+"="+toS(v))
	}
	return out
}

func toS(v any) string {
	switch x := v.(type) {
	case map[string]any:
		return strings.Join(mapStr(x), ",")
	case []any:
		var p []string
		for _, e := range x {
			p = append(p, toS(e))
		}
		return strings.Join(p, ",")
	case []string:
		return strings.Join(x, ",")
	case string:
		return x
	}
	return ""
}

// A pull of the task (with Asana's older value) between the create and the push does not
// drop the pending value, and after the push the multi_enum order is stable.
func TestAttackPullBetweenCreateAndValue(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf2", "C", now)
	_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf2", []string{local, "m2", "m1"}, now)
	for i := 0; i < 2; i++ {
		if err := s.syncCustomFields(ctx, "p1", true); err != nil {
			t.Fatal(err)
		}
		task, _ := s.API.Task(ctx, "t1")
		if err := st.UpsertTasks(ctx, []asana.Task{*task}); err != nil {
			t.Fatal(err)
		}
	}
	v := fieldOf(t, st, "t1", "cf2")
	if !slices.Contains(optionGIDs(v.Value), local) {
		t.Fatalf("pull dropped the value: %+v", v)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	got := optionGIDs(f.value("t1", "cf2"))
	slices.Sort(got)
	if !slices.Equal(got, []string{"m1", "m2", "new1"}) {
		t.Fatalf("Asana = %v", got)
	}
	noLocalGIDSent(t, f)
	v = fieldOf(t, st, "t1", "cf2")
	if gs := optionGIDs(v.Value); !slices.Equal(gs, []string{"m1", "m2", "new1"}) || v.Display != "A, B, C" {
		t.Fatalf("stored = %+v", v)
	}
}

// The mapping local → real option survives a prune while a value still has the local gid.
func TestAttackPruneKeepsOptionMapping(t *testing.T) {
	_, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if real, _ := st.RealEnumOptionGID(ctx, local); real != "new1" {
		t.Fatalf("real = %q", real)
	}
	late := now.Add(10 * store.ResultGrace)
	for i := 0; i < 2; i++ {
		_, _ = st.PruneResults(ctx, late)
	}
	// Nothing needs it now: after the grace it may go. Before that, it must stay.
	if _, err := st.PruneResults(ctx, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

// A field setting: add, remove (undoes), add again; and remove, add (undoes) keeps
// position and importance.
func TestAttackFieldSettingToggle(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ { // add, then remove: back to nothing queued
		if err := st.SetProjectFieldLocal(ctx, "p1", "cf3", i%2 == 0, now); err != nil {
			t.Fatal(i, err)
		}
	}
	if n, _, _, _ := st.OutboxCounts(ctx); n != 0 {
		t.Fatalf("%d ops queued", n)
	}
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf1", true, now); err == nil {
		t.Fatal("double add accepted")
	}
	if err := st.SetProjectFieldLocal(ctx, "nope", "cf3", true, now); err == nil {
		t.Fatal("unknown project accepted")
	}
	if err := st.SetProjectFieldLocal(ctx, "p1", "nope", true, now); err == nil {
		t.Fatal("unknown field accepted")
	}
	// remove cf1 then add it back: no request, same place.
	before, _ := st.ProjectFields(ctx, "p1")
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf1", false, now); err != nil {
		t.Fatal(err)
	}
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf1", true, now); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ProjectFields(ctx, "p1")
	if len(before) != len(after) {
		t.Fatalf("%v vs %v", before, after)
	}
	for i := range before {
		if before[i].GID != after[i].GID || before[i].Attached != after[i].Attached || before[i].Important != after[i].Important {
			t.Fatalf("%+v vs %+v", before[i], after[i])
		}
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if f.count("POST /projects/p1/") != 0 {
		t.Fatalf("calls %v", f.calls)
	}
}

// A pull of the project's fields in between keeps the queued add / remove.
func TestAttackFieldSettingSurvivesPull(t *testing.T) {
	_, s, st := setupOptions(t)
	ctx := context.Background()
	_ = st.SetProjectFieldLocal(ctx, "p1", "cf3", true, now)
	_ = st.SetProjectFieldLocal(ctx, "p1", "cf1", false, now)
	if err := s.syncCustomFields(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	fields, _ := st.ProjectFields(ctx, "p1")
	att := map[string]bool{}
	for _, pf := range fields {
		att[pf.GID] = pf.Attached
	}
	if !att["cf3"] || att["cf1"] || !att["cf2"] {
		t.Fatalf("attached = %v", att)
	}
}

// 400 on add/remove when the project is not as wanted (a real refusal) stays failed and
// does not drop the op; 400 when it already is: done.
func TestAttackFieldSetting400(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	_ = st.SetProjectFieldLocal(ctx, "p1", "cf3", true, now)
	f.status["POST /projects/p1/addCustomFieldSetting"] = 400 // refused, cf3 not on project
	res, err := s.Push(ctx)
	if err != nil || res.Failed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, _, failed, _ := st.OutboxCounts(ctx); failed != 1 {
		t.Fatal("not kept as failed")
	}
}

// 403 on one project / field does not lock another field or project.
func TestAttackForbiddenIsScoped(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	_, _, _ = st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	_, _, _ = st.AddEnumOptionLocal(ctx, "cf2", "C", now)
	f.status["POST /custom_fields/cf1/enum_options"] = 403
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if locked, _ := st.FieldOptionsLocked(ctx, "cf2"); locked {
		t.Fatal("cf2 locked")
	}
	if locked, _ := st.FieldOptionsLocked(ctx, "cf1"); !locked {
		t.Fatal("cf1 not locked")
	}
	if _, ok, _ := st.PlanRefusedAt(ctx, plan.CustomFields, ""); ok {
		t.Fatal("403 marked the plan unavailable")
	}
	if v, _ := st.SyncState(ctx, "plan_unavailable:"+plan.CustomFields); v != "" {
		t.Fatal("403 marked legacy global plan unavailable")
	}
	if f.count("POST /custom_fields/cf2/enum_options") != 1 {
		t.Fatalf("calls %v", f.calls)
	}
}

// 402/403 listing the workspace's fields hides them; another error leaves things as they were.
func TestAttackWorkspaceFieldsErrors(t *testing.T) {
	for _, code := range []int{402, 403, 500} {
		f, s, st := setupOptions(t)
		ctx := context.Background()
		f.status["GET /workspaces/w1/custom_fields"] = code
		if err := s.syncWorkspaceCustomFields(ctx, "w1", true); err != nil {
			t.Fatalf("%d: %v", code, err)
		}
		fields, _ := st.ProjectFields(ctx, "p1")
		var n int
		for _, pf := range fields {
			if !pf.Attached {
				n++
			}
		}
		if (code != 500) == (n != 0) && code != 500 {
			t.Fatalf("%d: %d offered", code, n)
		}
	}
}

// Offline (the server is gone): nothing is lost and no local gid is sent anywhere.
func TestAttackOffline(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now)
	_ = st.SetProjectFieldLocal(ctx, "p1", "cf3", true, now)
	dead := asana.NewClient(&http.Client{Timeout: 2 * time.Second})
	dead.BaseURL, dead.MaxRetries = "http://127.0.0.1:1", 0
	s.API = dead
	_, _ = s.Push(ctx)
	if n, c, fl, _ := st.OutboxCounts(ctx); n+c+fl != 3 {
		t.Fatalf("ops: %d %d %d", n, c, fl)
	}
	noLocalGIDSent(t, f)
}

func attachedOrder(t *testing.T, st *store.Store) []string {
	t.Helper()
	fields, err := st.ProjectFields(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, pf := range fields {
		if pf.Attached {
			out = append(out, pf.GID)
		}
	}
	return out
}

// The project's fields are shown in the order Asana gives (not by gid), and removing a
// field and then undoing it puts it back in its place.
func TestAttackProjectFieldOrderAndUndo(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	f.project = []string{"cf2", "cf3", "cf1"}
	if err := s.syncCustomFields(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	want := []string{"cf2", "cf3", "cf1"}
	if got := attachedOrder(t, st); !slices.Equal(got, want) {
		t.Fatalf("order after sync = %v, want %v", got, want)
	}
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf3", false, now); err != nil {
		t.Fatal(err)
	}
	if got := attachedOrder(t, st); !slices.Equal(got, []string{"cf2", "cf1"}) {
		t.Fatalf("after remove = %v", got)
	}
	if err := st.SetProjectFieldLocal(ctx, "p1", "cf3", true, now); err != nil {
		t.Fatal(err)
	}
	if got := attachedOrder(t, st); !slices.Equal(got, want) {
		t.Fatalf("after undo = %v, want %v", got, want)
	}
}

// A sync fetched the option (Asana created it, the answer was lost) before the push
// records it: the field ends with the option once, not twice.
func TestAttackNoDuplicateOptionAfterPull(t *testing.T) {
	f, s, st := setupOptions(t)
	ctx := context.Background()
	local, _, _ := st.AddEnumOptionLocal(ctx, "cf1", "低", now)
	_, _ = st.SetCustomFieldLocal(ctx, "t1", "cf1", local, now)
	yes := true
	f.fields["cf1"].EnumOptions = append(f.fields["cf1"].EnumOptions, asana.EnumOption{GID: "o9", Name: "低", Enabled: &yes})
	if err := s.syncCustomFields(ctx, "p1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Push(ctx); err != nil {
		t.Fatal(err)
	}
	v := fieldOf(t, st, "t1", "cf1")
	n := 0
	for _, o := range v.Options {
		if o.GID == "o9" {
			n++
		}
		if o.GID == local {
			t.Fatalf("local option left: %+v", v.Options)
		}
	}
	if n != 1 || v.Value != "o9" {
		t.Fatalf("options = %+v value %v", v.Options, v.Value)
	}
}
