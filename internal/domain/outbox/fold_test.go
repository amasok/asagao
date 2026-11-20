package outbox

import (
	"maps"
	"reflect"
	"testing"
)

type m = map[string]any

func TestFold(t *testing.T) {
	cur := m{"name": "元の名前", "completed": false, "due_on": "2026-11-20", "notes": "", "resource_subtype": "approval"}
	for _, tc := range []struct {
		name     string
		op       Op
		changes  m
		want     Op
		deleteOp bool
	}{
		{
			name:    "a new entry takes the base from the task",
			op:      Op{State: StatePending},
			changes: m{"name": "新しい名前"},
			want:    Op{State: StatePending, Payload: m{"name": "新しい名前"}, Base: m{"name": "元の名前"}},
		},
		{
			name:     "a new entry with no change from the task is not saved",
			op:       Op{State: StatePending},
			changes:  m{"name": "元の名前"},
			want:     Op{State: StatePending, Payload: m{}, Base: m{"name": "元の名前"}},
			deleteOp: true,
		},
		{
			name:    "joining keeps the first base",
			op:      Op{ID: 3, State: StatePending, Payload: m{"name": "二番目"}, Base: m{"name": "元の名前"}},
			changes: m{"name": "三番目"},
			want:    Op{ID: 3, State: StatePending, Payload: m{"name": "三番目"}, Base: m{"name": "元の名前"}},
		},
		{
			name:    "another field joins with its own base",
			op:      Op{ID: 3, State: StatePending, Payload: m{"name": "二番目"}, Base: m{"name": "元の名前"}},
			changes: m{"due_on": ""},
			want:    Op{ID: 3, State: StatePending, Payload: m{"name": "二番目", "due_on": ""}, Base: m{"name": "元の名前", "due_on": "2026-11-20"}},
		},
		{
			name:     "changing back to the base empties the entry",
			op:       Op{ID: 3, State: StatePending, Payload: m{"name": "二番目"}, Base: m{"name": "元の名前"}},
			changes:  m{"name": "元の名前"},
			want:     Op{ID: 3, State: StatePending, Payload: m{}, Base: m{"name": "元の名前"}},
			deleteOp: true,
		},
		{
			name:    "changing one field back leaves the others",
			op:      Op{ID: 3, State: StatePending, Payload: m{"name": "二番目", "completed": true}, Base: m{"name": "元の名前", "completed": false}},
			changes: m{"completed": false},
			want:    Op{ID: 3, State: StatePending, Payload: m{"name": "二番目"}, Base: m{"name": "元の名前", "completed": false}},
		},
		{
			name:     "the base is compared as printed (a JSON number)",
			op:       Op{ID: 3, State: StatePending, Payload: m{"x": 2.0}, Base: m{"x": float64(1)}},
			changes:  m{"x": 1},
			want:     Op{ID: 3, State: StatePending, Payload: m{}, Base: m{"x": float64(1)}},
			deleteOp: true,
		},
		{
			name:     "a conflict on a field changed back is dropped, and the entry is pending",
			op:       Op{ID: 4, State: StateConflict, Payload: m{"name": "ローカル"}, Base: m{"name": "元の名前"}, Conflict: m{"name": "サーバー"}},
			changes:  m{"name": "元の名前"},
			want:     Op{ID: 4, State: StatePending, Payload: m{}, Base: m{"name": "元の名前"}, Conflict: m{}},
			deleteOp: true,
		},
		{
			name:    "a conflict on another field stays",
			op:      Op{ID: 4, State: StateConflict, Payload: m{"name": "ローカル", "notes": "x"}, Base: m{"name": "元の名前", "notes": ""}, Conflict: m{"name": "サーバー"}},
			changes: m{"notes": ""},
			want:    Op{ID: 4, State: StateConflict, Payload: m{"name": "ローカル"}, Base: m{"name": "元の名前", "notes": ""}, Conflict: m{"name": "サーバー"}},
		},
		{
			name:    "a conflicted field changed again stays conflicted",
			op:      Op{ID: 4, State: StateConflict, Payload: m{"name": "ローカル"}, Base: m{"name": "元の名前"}, Conflict: m{"name": "サーバー"}},
			changes: m{"name": "もう一度"},
			want:    Op{ID: 4, State: StateConflict, Payload: m{"name": "もう一度"}, Base: m{"name": "元の名前"}, Conflict: m{"name": "サーバー"}},
		},
		{
			name:    "a conflicted entry without conflicting fields is pending",
			op:      Op{ID: 4, State: StateConflict, Payload: m{"name": "ローカル"}, Base: m{"name": "元の名前"}},
			changes: m{"notes": "メモ"},
			want:    Op{ID: 4, State: StatePending, Payload: m{"name": "ローカル", "notes": "メモ"}, Base: m{"name": "元の名前", "notes": ""}},
		},
		{
			name:    "deciding an approval keeps completed in the base",
			op:      Op{State: StatePending},
			changes: m{"approval_status": "approved"},
			want:    Op{State: StatePending, Payload: m{"approval_status": "approved"}, Base: m{"approval_status": nil, "completed": false}},
		},
		{
			name:    "an approval keeps the completed base already there",
			op:      Op{ID: 5, State: StatePending, Payload: m{"completed": true}, Base: m{"completed": false}},
			changes: m{"approval_status": "rejected"},
			want:    Op{ID: 5, State: StatePending, Payload: m{"completed": true, "approval_status": "rejected"}, Base: m{"completed": false, "approval_status": nil}},
		},
		{
			name:    "a field the task does not have has a nil base",
			op:      Op{State: StatePending},
			changes: m{"assignee": "1200"},
			want:    Op{State: StatePending, Payload: m{"assignee": "1200"}, Base: m{"assignee": nil}},
		},
		{
			name:    "the other fields of the entry are kept",
			op:      Op{ID: 6, Kind: KindUpdateTask, TaskGID: "1200", TaskName: "t", BaseModifiedAt: "2026-11-01T00:00:00Z", State: StatePending, CreatedAt: "c", Payload: m{}, Base: m{}},
			changes: m{"name": "x"},
			want:    Op{ID: 6, Kind: KindUpdateTask, TaskGID: "1200", TaskName: "t", BaseModifiedAt: "2026-11-01T00:00:00Z", State: StatePending, CreatedAt: "c", Payload: m{"name": "x"}, Base: m{"name": "元の名前"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := cloneOp(tc.op)
			got, deleteOp := Fold(tc.op, cur, tc.changes)
			if deleteOp != tc.deleteOp {
				t.Errorf("deleteOp = %v, want %v", deleteOp, tc.deleteOp)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Fold =\n  %+v\nwant\n  %+v", got, tc.want)
			}
			if !reflect.DeepEqual(tc.op, before) {
				t.Errorf("Fold changed its argument: %+v, was %+v", tc.op, before)
			}
		})
	}
}

func cloneOp(op Op) Op {
	op.Payload, op.Base, op.Conflict = maps.Clone(op.Payload), maps.Clone(op.Base), maps.Clone(op.Conflict)
	return op
}
