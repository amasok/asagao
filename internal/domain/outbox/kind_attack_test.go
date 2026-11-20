package outbox

import (
	"encoding/json"
	"math"
	"slices"
	"testing"
)

// The strings stored in existing store.db files (copied from main before #70): a kind
// whose constant changed value would orphan every queued entry of it.
var storedKinds = map[Kind]string{
	KindUpdateTask: "update_task", KindCreateTask: "create_task", KindAddComment: "add_comment",
	KindMoveTask: "move_task", KindRenameSection: "rename_section", KindCreateSection: "create_section",
	KindDeleteSection: "delete_section", KindInsertSection: "insert_section", KindUpdateTag: "update_tag",
	KindDeleteTag: "delete_tag", KindCreateStatus: "create_status", KindAddTag: "add_tag",
	KindRemoveTag: "remove_tag", KindAddFollower: "add_follower", KindRemoveFollower: "remove_follower",
	KindAddProject: "add_project", KindRemoveProject: "remove_project", KindDuplicateTask: "duplicate_task",
	KindUploadAttachment: "upload_attachment", KindDeleteTask: "delete_task", KindSetParent: "set_parent",
	KindAddDependency: "add_dependency", KindRemoveDependency: "remove_dependency",
	KindSetCustomField: "set_custom_field", KindMarkNotification: "mark_notification",
	KindUpdateStory: "update_story", KindDeleteStory: "delete_story", KindDeleteAttachment: "delete_attachment",
	KindMoveMySection: "move_my_section", KindUpdateProject: "update_project",
	KindUpdateProjectBrief: "update_project_brief", KindAddTimeEntry: "add_time_entry",
	KindUpdateTimeEntry: "update_time_entry", KindDeleteTimeEntry: "delete_time_entry",
	KindCreateEnumOption: "create_enum_option", KindAddFieldSetting: "add_custom_field_setting",
	KindRemoveFieldSetting: "remove_custom_field_setting",
}

func TestAttackStoredKindStringsUnchanged(t *testing.T) {
	if len(storedKinds) != 37 {
		t.Fatalf("%d golden kinds", len(storedKinds))
	}
	var want []Kind
	for k, s := range storedKinds {
		if string(k) != s {
			t.Errorf("%q changed its stored value to %q", s, k)
		}
		want = append(want, Kind(s))
	}
	slices.Sort(want)
	if !slices.Equal(want, AllKinds()) {
		t.Errorf("AllKinds %v differs from the stored kinds", AllKinds())
	}
	// A kind survives JSON (Op is serialised in places) as its plain string.
	b, err := json.Marshal(Op{Kind: KindAddFieldSetting})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil || m["kind"] != "add_custom_field_setting" {
		t.Errorf("json %s", b)
	}
}

// What the switches of main said, kind by kind.
func TestAttackSpecsMatchTheOldSwitches(t *testing.T) {
	// SafeToResend: these were "not safe", the rest safe.
	oldCreates := []Kind{KindCreateTask, KindDuplicateTask, KindAddComment, KindUploadAttachment, KindCreateStatus,
		KindCreateSection, KindAddTimeEntry, KindCreateEnumOption}
	// waitsForCreate returned false at once for these (no task is waited for).
	oldNoTaskWait := []Kind{KindDuplicateTask, KindCreateStatus, KindRenameSection, KindMarkNotification,
		KindCreateSection, KindDeleteSection, KindInsertSection, KindUpdateProject, KindCreateEnumOption,
		KindAddFieldSetting, KindRemoveFieldSetting, KindUpdateProjectBrief, KindUpdateTag, KindDeleteTag}
	// dropGone discarded these as "gone" without looking at a task.
	oldGoneOp := []Kind{KindRenameSection, KindCreateStatus, KindMarkNotification, KindUpdateProject,
		KindCreateSection, KindDeleteSection, KindInsertSection, KindUpdateTag, KindDeleteTag,
		KindCreateEnumOption, KindAddFieldSetting, KindRemoveFieldSetting, KindUpdateProjectBrief}
	for _, k := range AllKinds() {
		s, _ := SpecOf(k)
		if s.Creates != slices.Contains(oldCreates, k) {
			t.Errorf("%s: Creates = %v", k, s.Creates)
		}
		safe, err := SafeToResend(Op{Kind: k, Payload: map[string]any{"tag": "t"}}, func(string) (bool, error) { return true, nil })
		if err != nil || safe == slices.Contains(oldCreates, k) {
			t.Errorf("%s: SafeToResend = %v, %v", k, safe, err)
		}
		op := Op{Kind: k, TaskGID: "local:x", Payload: map[string]any{"parent": "local:p"}}
		deps := TaskDeps(op)
		switch {
		case slices.Contains(oldNoTaskWait, k):
			if len(deps) != 0 {
				t.Errorf("%s waits for tasks %v", k, deps)
			}
		case k == KindCreateTask:
			if !slices.Equal(deps, []string{"local:p"}) {
				t.Errorf("create_task deps %v", deps)
			}
		case k == KindSetParent:
			if !slices.Equal(deps, []string{"local:p", "local:x"}) {
				t.Errorf("set_parent deps %v", deps)
			}
		default:
			if !slices.Equal(deps, []string{"local:x"}) {
				t.Errorf("%s deps %v", k, deps)
			}
		}
		// the dropGone split: new task / not a task / a task
		goneOp := s.Subject != SubjectTask && s.Subject != SubjectNewTask
		if goneOp != slices.Contains(oldGoneOp, k) {
			t.Errorf("%s: dropGone as a non-task entry = %v", k, goneOp)
		}
	}
	// set_parent to the top level, and with an empty or non-string parent
	if got := TaskDeps(Op{Kind: KindSetParent, TaskGID: "t", Payload: map[string]any{"parent": ""}}); !slices.Equal(got, []string{"t"}) {
		t.Errorf("set_parent to top level: %v", got)
	}
	if got := TaskDeps(Op{Kind: KindSetParent, TaskGID: "t", Payload: map[string]any{"parent": 5.0}}); !slices.Equal(got, []string{"t"}) {
		t.Errorf("set_parent with a number: %v", got)
	}
	if got := TaskDeps(Op{Kind: KindCreateTask, TaskGID: "local:n", Payload: nil}); got != nil {
		t.Errorf("create_task without payload: %v", got)
	}
	if got := TaskDeps(Op{Kind: "from_the_future", TaskGID: "t"}); got != nil {
		t.Errorf("unknown kind: %v", got)
	}
}

// Subjects the old code assumed for task_gid.
func TestAttackSubjects(t *testing.T) {
	want := map[Kind]Subject{
		KindRenameSection: SubjectSection, KindCreateSection: SubjectSection, KindDeleteSection: SubjectSection,
		KindInsertSection: SubjectSection, KindUpdateTag: SubjectTag, KindDeleteTag: SubjectTag,
		KindCreateStatus: SubjectProject, KindUpdateProject: SubjectProject, KindUpdateProjectBrief: SubjectProject,
		KindAddFieldSetting: SubjectProject, KindRemoveFieldSetting: SubjectProject,
		KindMarkNotification: SubjectNotification, KindCreateEnumOption: SubjectEnumOption,
		KindCreateTask: SubjectNewTask, KindDuplicateTask: SubjectNewTask,
	}
	for _, k := range AllKinds() {
		w, ok := want[k]
		if !ok {
			w = SubjectTask
		}
		if SubjectOf(k) != w || k.OnTask() != (w == SubjectTask) {
			t.Errorf("%s subject %v", k, SubjectOf(k))
		}
	}
}

// DEFECT: numbers of any Go type are compared as float64, so two different integers above
// 2^53 are "the same". main compared the printed text and told them apart.
func TestAttackSameValueBigIntegers(t *testing.T) {
	for _, tc := range []struct{ a, b any }{
		{int64(1<<53 + 1), int64(1 << 53)},
		{int64(math.MaxInt64), int64(math.MaxInt64 - 1)},
		{uint64(math.MaxUint64), uint64(math.MaxUint64 - 1)},
		{int64(1<<62 + 1), 1 << 62},
	} {
		if SameValue(tc.a, tc.b) {
			t.Errorf("SameValue(%v, %v) = true", tc.a, tc.b)
		}
	}
}

func TestAttackSameValueEdges(t *testing.T) {
	type named []string
	type namedMap map[string]int
	for _, tc := range []struct {
		name string
		a, b any
		want bool
	}{
		{"-0 and 0", math.Copysign(0, -1), 0.0, true},
		{"inf", math.Inf(1), math.Inf(1), true},
		{"inf and -inf", math.Inf(1), math.Inf(-1), false},
		{"float32 NaN", float32(math.NaN()), math.NaN(), false},
		{"named slice", named{"a"}, []any{"a"}, true},
		{"named map", namedMap{"a": 1}, map[string]any{"a": 1.0}, true},
		{"array", [2]string{"a", "b"}, []any{"a", "b"}, true},
		{"nil inside list", []any{nil}, []any{nil}, true},
		{"nil inside list vs text", []any{nil}, []any{"<nil>"}, false},
		{"empty map vs nil map", map[string]any{}, map[string]any(nil), false},
		{"empty list vs empty map", []any{}, map[string]any{}, false},
		{"nil interface in []any vs nil slice", []any{[]string(nil)}, []any{nil}, true},
		{"bool vs 1", true, 1, false},
		{"string vs named bool", "true", namedBool(true), false},
		{"json.Number huge", json.Number("1e999"), "1e999", true}, // falls back to its text
		{"json.Number vs int", json.Number("3"), 3, true},
		{"json.Number invalid", json.Number("x"), 3, false},
		{"pointer to value", ptr("a"), "a", false}, // pointers are not written to the outbox
		{"non-string keys", map[int]string{1: "a"}, map[int]string{1: "a"}, true},
		{"non-string keys differ", map[int]string{1: "a"}, map[int]string{1: "b"}, false},
		{"struct", struct{ A int }{1}, struct{ A int }{1}, true},
		{"deep list", []any{[]any{[]any{1}}}, []any{[]any{[]any{1.0}}}, true},
		{"deep list differs", []any{[]any{[]any{1}}}, []any{[]any{[]any{2.0}}}, false},
		{"map with a missing and an extra key", map[string]any{"a": 1}, map[string]any{"b": 1, "a": 1}, false},
	} {
		if got := SameValue(tc.a, tc.b); got != tc.want {
			t.Errorf("%s: SameValue(%#v, %#v) = %v, want %v", tc.name, tc.a, tc.b, got, tc.want)
		}
		if got := SameValue(tc.b, tc.a); got != tc.want {
			t.Errorf("%s swapped: = %v, want %v", tc.name, got, tc.want)
		}
	}
}

type namedBool bool

func ptr[T any](v T) *T { return &v }

// Fold through JSON: a base read back from the outbox's JSON column against live values.
func TestAttackFoldAgainstJSONBase(t *testing.T) {
	var base map[string]any
	if err := json.Unmarshal([]byte(`{"custom_fields":{"f":["a","b"]},"n":3,"done":false,"due_on":null,"notes":""}`), &base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		field string
		v     any
		del   bool // equal to the base: the change is no change
	}{
		{"custom_fields", map[string]any{"f": []string{"a", "b"}}, true},
		{"custom_fields", map[string]any{"f": []string{"b", "a"}}, false},
		{"n", 3, true},
		{"n", "3", false},
		{"n", 3.5, false},
		{"done", false, true},
		{"done", "false", false},
		{"due_on", nil, true},
		{"due_on", "", false}, // "" clears, nil says nothing
		{"due_on", "<nil>", false},
		{"notes", "", true},
		{"notes", nil, false},
	} {
		_, del := Fold(Op{Payload: map[string]any{tc.field: "x"}, Base: base}, nil, map[string]any{tc.field: tc.v})
		if del != tc.del {
			t.Errorf("%s = %#v: deleted = %v, want %v", tc.field, tc.v, del, tc.del)
		}
	}
}
