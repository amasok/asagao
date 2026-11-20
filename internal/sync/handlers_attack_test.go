package sync

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	domaintask "github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/store"
)

// oldPlanWorkspace is planWorkspace as it was in main (a switch on the kind).
func oldPlanWorkspace(ctx context.Context, s *Syncer, op outbox.Op) string {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	var ws string
	switch op.Kind {
	case outbox.KindCreateSection, outbox.KindDeleteSection, outbox.KindInsertSection:
		ws, _ = s.Store.ProjectWorkspace(ctx, str("project"))
	case outbox.KindMoveMySection:
		if ws = str("workspace"); ws == "" {
			ws, _ = s.Store.TaskWorkspace(ctx, op.TaskGID)
		}
	case outbox.KindUpdateTag:
		ws, _ = s.Store.TagWorkspace(ctx, op.TaskGID)
	case outbox.KindDeleteTag:
		if ws = str("workspace"); ws == "" {
			tasks, _ := op.Payload["tasks"].([]any)
			for _, t := range tasks {
				if g, _ := t.(string); g != "" {
					if ws, _ = s.Store.TaskWorkspace(ctx, g); ws != "" {
						break
					}
				}
			}
		}
	}
	if ws == "" {
		ws, _ = s.Store.DefaultWorkspace(ctx)
	}
	return ws
}

func attackWorld(t *testing.T) (*Syncer, *store.Store) {
	t.Helper()
	_, _, s, st := setupPush(t)
	ctx := context.Background()
	if err := st.UpsertProjects(ctx, "w2", []model.Project{{GID: "p2", Name: "Q"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []model.Task{{GID: "t2", Name: "B", Workspace: &model.Ref{GID: "w2"}}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTags(ctx, []model.Tag{{GID: "g2", Name: "x", Workspace: &model.Ref{GID: "w2"}}}); err != nil {
		t.Fatal(err)
	}
	return s, st
}

// Every kind answers planWorkspace as main did, for payloads that give, miss or garble
// the keys it reads.
func TestAttackPlanWorkspaceEveryKind(t *testing.T) {
	s, _ := attackWorld(t)
	ctx := context.Background()
	payloads := []map[string]any{
		nil,
		{},
		{"project": "p2"},
		{"project": "nope"},
		{"project": 5.0},
		{"workspace": "w9"},
		{"workspace": ""},
		{"workspace": 5.0},
		{"tasks": []any{"t2"}},
		{"tasks": []any{"", "t2", "t1"}},
		{"tasks": []any{5.0, "t1"}},
		{"tasks": []string{"t2"}}, // not what JSON gives: ignored by both
		{"tasks": "t2"},
	}
	for _, k := range outbox.AllKinds() {
		for _, gid := range []string{"", "t1", "t2", "g2", "p2", "local:zz"} {
			for _, p := range payloads {
				op := outbox.Op{Kind: k, TaskGID: gid, Payload: p}
				if got, want := s.planWorkspace(ctx, op), oldPlanWorkspace(ctx, s, op); got != want {
					t.Errorf("%s gid=%q payload=%v: %q, want %q", k, gid, p, got, want)
				}
			}
		}
	}
	// and for an entry of a kind from the future
	if got, want := s.planWorkspace(ctx, outbox.Op{Kind: "x", TaskGID: "t2"}), oldPlanWorkspace(ctx, s, outbox.Op{Kind: "x", TaskGID: "t2"}); got != want {
		t.Errorf("unknown kind: %q, want %q", got, want)
	}
}

// What waitsForCreate said in main for an entry on a task whose create is still queued:
// everything waits except the kinds that name no task. The queued create is a local task
// (gid L); the entry is built the way the app builds it.
func TestAttackWaitsForCreateEveryKind(t *testing.T) {
	s, st := attackWorld(t)
	ctx := context.Background()
	local, err := st.CreateTaskLocal(ctx, store.NewTask{WorkspaceGID: "w1", ProjectGID: "p1", Name: "offline"}, time.Now())
	if err != nil || !localgid.IsLocal(local) {
		t.Fatalf("local task %q %v", local, err)
	}
	noWait := []outbox.Kind{outbox.KindDuplicateTask, outbox.KindCreateStatus, outbox.KindRenameSection,
		outbox.KindMarkNotification, outbox.KindCreateSection, outbox.KindDeleteSection, outbox.KindInsertSection,
		outbox.KindUpdateProject, outbox.KindCreateEnumOption, outbox.KindAddFieldSetting,
		outbox.KindRemoveFieldSetting, outbox.KindUpdateProjectBrief, outbox.KindUpdateTag, outbox.KindDeleteTag,
		outbox.KindCreateTask} // a create waits only for its parent
	for _, k := range outbox.AllKinds() {
		op := outbox.Op{Kind: k, TaskGID: local, Payload: map[string]any{}}
		got, err := s.waitsForCreate(ctx, op)
		if err != nil {
			t.Errorf("%s: %v", k, err)
			continue
		}
		if want := !slices.Contains(noWait, k); got != want {
			t.Errorf("%s on a task whose create is queued: waits = %v, want %v", k, got, want)
		}
		// the same entry on a task that exists in Asana never waits
		op.TaskGID = "t1"
		if got, err := s.waitsForCreate(ctx, op); err != nil || got {
			t.Errorf("%s on a real task: waits = %v, %v", k, got, err)
		}
	}
	// create_task and set_parent wait for the parent, not (create) for the entry's own gid
	cases := []struct {
		name string
		op   outbox.Op
		want bool
	}{
		{"subtask of an unsent task", outbox.Op{Kind: outbox.KindCreateTask, TaskGID: "local:new", Payload: map[string]any{"parent": local}}, true},
		{"subtask of a real task", outbox.Op{Kind: outbox.KindCreateTask, TaskGID: local, Payload: map[string]any{"parent": "t1"}}, false},
		{"top-level create", outbox.Op{Kind: outbox.KindCreateTask, TaskGID: local, Payload: map[string]any{}}, false},
		{"parent is not a string", outbox.Op{Kind: outbox.KindCreateTask, TaskGID: local, Payload: map[string]any{"parent": 7.0}}, false},
		{"move under an unsent parent", outbox.Op{Kind: outbox.KindSetParent, TaskGID: "t1", Payload: map[string]any{"parent": local}}, true},
		{"move an unsent task", outbox.Op{Kind: outbox.KindSetParent, TaskGID: local, Payload: map[string]any{"parent": "t1"}}, true},
		{"move an unsent task to the top", outbox.Op{Kind: outbox.KindSetParent, TaskGID: local, Payload: map[string]any{"parent": ""}}, true},
		{"move a real task to the top", outbox.Op{Kind: outbox.KindSetParent, TaskGID: "t1", Payload: map[string]any{"parent": ""}}, false},
		{"real under real", outbox.Op{Kind: outbox.KindSetParent, TaskGID: "t1", Payload: map[string]any{"parent": "t2"}}, false},
		{"local gid whose create is gone", outbox.Op{Kind: outbox.KindUpdateTask, TaskGID: "local:ghost", Payload: map[string]any{}}, false},
	}
	for _, tc := range cases {
		if got, err := s.waitsForCreate(ctx, tc.op); err != nil || got != tc.want {
			t.Errorf("%s: waits = %v, %v; want %v", tc.name, got, err, tc.want)
		}
	}
	// A delete_section waits for the deletes of tasks queued in it, and nothing else does.
	if got, err := s.waitsForCreate(ctx, outbox.Op{Kind: outbox.KindDeleteSection, TaskGID: "s1", Payload: map[string]any{"project": "p1"}}); err != nil || got {
		t.Errorf("delete_section with nothing queued: %v %v", got, err)
	}
	if _, err := st.DeleteTaskLocal(ctx, "t1", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, k := range outbox.AllKinds() {
		got, err := s.waitsForCreate(ctx, outbox.Op{Kind: k, TaskGID: "s1", Payload: map[string]any{"project": "p1"}})
		if err != nil {
			t.Fatal(err)
		}
		if want := k == outbox.KindDeleteSection; got != want {
			t.Errorf("%s while a task of section s1 is being deleted: waits = %v", k, got)
		}
	}
}

// oldBatchable is batchEntryFor's yes/no as main's switch said it, for a task that is
// in Asana.
func oldBatchable(ctx context.Context, s *Syncer, op outbox.Op) (bool, error) {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	real := func(gid string) bool { return gid != "" && !localgid.IsLocal(gid) }
	if op.TaskGID == "" || localgid.IsLocal(op.TaskGID) {
		return false, nil
	}
	switch op.Kind {
	case outbox.KindUpdateTask:
		return !domaintask.TouchesCustomType(op.Payload), nil
	case outbox.KindSetCustomField:
		if str("field") == "" {
			return false, nil
		}
		wait, err := s.waitsForField(ctx, op)
		return !wait && err == nil, err
	case outbox.KindMoveTask:
		return real(str("section")) && str("insert_before") == "" && str("insert_after") == "", nil
	case outbox.KindAddTag, outbox.KindRemoveTag:
		tag, err := s.Store.RealTagGID(ctx, str("tag"))
		return real(tag), err
	case outbox.KindAddFollower, outbox.KindRemoveFollower:
		return real(str("user")), nil
	case outbox.KindAddProject:
		return real(str("project")) && (str("section") == "" || real(str("section"))), nil
	case outbox.KindRemoveProject:
		return real(str("project")), nil
	case outbox.KindAddDependency, outbox.KindRemoveDependency:
		dep, err := s.Store.RealGID(ctx, str("dependency"))
		return real(dep), err
	}
	return false, nil
}

// Which entries can go out in a batch is what main decided, for every kind and for
// payloads at the edges.
func TestAttackBatchableEveryKind(t *testing.T) {
	s, _ := attackWorld(t)
	ctx := context.Background()
	vals := []any{nil, "", "x1", "local:a", 5.0}
	var payloads []map[string]any
	for _, v := range vals {
		for _, w := range vals {
			payloads = append(payloads, map[string]any{"section": v, "project": w, "tag": v, "user": w, "dependency": v, "field": w, "insert_before": w, "insert_after": v})
		}
	}
	payloads = append(payloads,
		map[string]any{"resource_subtype": "custom", "name": "x"},
		map[string]any{"custom_type": "c", "name": "x"},
		map[string]any{"name": "x"},
		map[string]any{"section": "s2", "insert_after": "t9"},
		map[string]any{"section": "s2", "insert_before": ""},
		map[string]any{"project": "p1", "section": "local:s"},
		map[string]any{"field": "f1", "value": "o1"},
		map[string]any{"field": "f1", "value": "local:opt"},
		map[string]any{"field": "f1", "value": []any{"local:opt"}},
	)
	for _, k := range outbox.AllKinds() {
		for _, gid := range []string{"t1", "", "local:x"} {
			for _, p := range payloads {
				op := outbox.Op{ID: 1, Kind: k, TaskGID: gid, Payload: p}
				want, werr := oldBatchable(ctx, s, op)
				e, got, err := s.batchEntryFor(ctx, op)
				if (err != nil) != (werr != nil) || got != want {
					t.Errorf("%s gid=%q payload=%v: batch = %v (%v), want %v (%v)", k, gid, p, got, err, want, werr)
					continue
				}
				if got && (e.write.Kind == "" || e.task != gid) {
					t.Errorf("%s: batched without a request: %+v", k, e)
				}
				if got && e.check != nil && k != outbox.KindUpdateTask && k != outbox.KindSetCustomField {
					t.Errorf("%s is checked against Asana before it is sent", k)
				}
				if got && (k == outbox.KindUpdateTask || k == outbox.KindSetCustomField) && e.check == nil {
					t.Errorf("%s is batched without the check against Asana", k)
				}
				if got && (e.refresh != (k == outbox.KindMoveTask)) {
					t.Errorf("%s: refresh = %v", k, e.refresh)
				}
			}
		}
	}
}

// Only the kinds main handled get a start-date plan check, a sendAlong or a settle time.
func TestAttackHandlerFlagsMatchMain(t *testing.T) {
	for _, k := range outbox.AllKinds() {
		h := handlers[k]
		if h.startOn != (k == outbox.KindUpdateTask || k == outbox.KindCreateTask) {
			t.Errorf("%s startOn = %v", k, h.startOn)
		}
		if (h.sendAlong != nil) != (k == outbox.KindMarkNotification) {
			t.Errorf("%s sendAlong set = %v", k, h.sendAlong != nil)
		}
		want := time.Minute
		if k == outbox.KindDuplicateTask {
			want = 5 * time.Minute
		}
		if got := settleTime(k); got != want {
			t.Errorf("%s settles in %v", k, got)
		}
		// main looked something up in Asana for exactly these kinds (the rest said
		// "not found" without a request)
		finds := []outbox.Kind{outbox.KindCreateSection, outbox.KindCreateEnumOption, outbox.KindCreateTask,
			outbox.KindDuplicateTask, outbox.KindAddComment, outbox.KindAddTimeEntry, outbox.KindUploadAttachment,
			outbox.KindCreateStatus, outbox.KindAddTag}
		if (h.find != nil) != slices.Contains(finds, k) {
			t.Errorf("%s find set = %v", k, h.find != nil)
		}
		// main's waitsForCreate special cases
		if (h.waits != nil) != (k == outbox.KindDeleteSection || k == outbox.KindUpdateTag || k == outbox.KindDeleteTag) {
			t.Errorf("%s waits set = %v", k, h.waits != nil)
		}
		// main handled a 403 specially for these three kinds only
		if (h.forbidden != nil) && !slices.Contains([]outbox.Kind{outbox.KindCreateEnumOption, outbox.KindAddFieldSetting,
			outbox.KindRemoveFieldSetting, outbox.KindUpdateProjectBrief, outbox.KindUpdateProject, outbox.KindCreateSection}, k) {
			t.Logf("%s has a forbidden handler (check it is intended)", k)
		}
	}
}
