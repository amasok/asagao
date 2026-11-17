package mcpserver

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

func updateOps(t *testing.T, st *store.Store) []store.Op {
	t.Helper()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []store.Op
	for _, op := range ops {
		if op.Kind == store.OpUpdateTask {
			out = append(out, op)
		}
	}
	return out
}

// With several invalid fields, the error is the first one in the order resource_subtype,
// approval_status, due_on, assignee, worded as before, and nothing is written.
func TestUpdateTaskErrorOrderAndWording(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st})
	_, errSub := task.ParseSubtype("bug")
	_, errAp := task.ParseApprovalStatus("maybe")
	bad := map[string]any{"gid": "t1", "resource_subtype": "bug", "approval_status": "maybe", "due_on": "someday", "assignee": "誰か", "name": "x"}
	without := func(keys ...string) map[string]any {
		m := map[string]any{}
		for k, v := range bad {
			m[k] = v
		}
		for _, k := range keys {
			delete(m, k)
		}
		return m
	}
	for _, tc := range []struct {
		name string
		args map[string]any
		want string // the whole message
	}{
		{"all", bad, "resource_subtype: " + errSub.Error()},
		{"no subtype", without("resource_subtype"), "approval_status: " + errAp.Error()},
		{"due and assignee", without("resource_subtype", "approval_status"), "due_on: "},
		{"assignee only", without("resource_subtype", "approval_status", "due_on"), `user "誰か"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := take(t, st, "t1")
			msg := toolError(call(t, cs, "update_task", tc.args, nil))
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("error = %q, want %q", msg, tc.want)
			}
			if strings.HasPrefix(msg, "assignee:") || strings.Contains(msg, "due: ") {
				t.Fatalf("wording changed: %q", msg)
			}
			if after := take(t, st, "t1"); !reflect.DeepEqual(before, after) {
				t.Fatalf("a failed update wrote: %+v", after.Ops)
			}
		})
	}
}

// liked and the other fields go into one queued update, one entry, as one write.
func TestUpdateTaskLikedSharesTheEntry(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st})
	call(t, cs, "update_task", map[string]any{"gid": "t1", "name": "新", "liked": true, "completed": true, "notes": "**本文**"}, nil)
	ops := updateOps(t, st)
	if len(ops) != 1 {
		t.Fatalf("%d update entries: %+v", len(ops), ops)
	}
	p := ops[0].Payload
	if p["liked"] != true || p["name"] != "新" || p["completed"] != true {
		t.Fatalf("payload = %+v", p)
	}
	if p["html_notes"] != richtext.TaskNotes.FromMarkdown("**本文**") || p["notes"] != richtext.TaskNotes.PlainFromMarkdown("**本文**") {
		t.Fatalf("notes = %+v", p)
	}
}

// Fields that are only a tag or a follower write no empty update entry; a call that changes
// nothing at all is refused with the same sentence.
func TestUpdateTaskWithoutFieldsQueuesNoUpdate(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st})
	call(t, cs, "update_task", map[string]any{"gid": "t1", "add_tags": []string{"新タグ"}}, nil)
	if ops := updateOps(t, st); len(ops) != 0 {
		t.Fatalf("empty update queued: %+v", ops)
	}
	msg := toolError(call(t, cs, "update_task", map[string]any{"gid": "t1"}, nil))
	if msg != "nothing to update: pass at least one field to change" {
		t.Fatalf("message = %q", msg)
	}
	// false / empty values are still values
	call(t, cs, "update_task", map[string]any{"gid": "t1", "completed": false}, nil)
	call(t, cs, "update_task", map[string]any{"gid": "t1", "due_on": "", "assignee": ""}, nil)
	ops := updateOps(t, st)
	if len(ops) != 1 {
		t.Fatalf("ops: %+v", ops)
	}
	if ops[0].Payload["due_on"] != "" || ops[0].Payload["assignee"] != "" {
		t.Fatalf("clear not queued: %+v", ops[0].Payload)
	}
}

// Notes "" clears the description (a value, not "no change").
func TestUpdateTaskEmptyNotesClears(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st})
	call(t, cs, "update_task", map[string]any{"gid": "t1", "notes": ""}, nil)
	ops := updateOps(t, st)
	if len(ops) != 1 {
		t.Fatalf("ops: %+v", ops)
	}
	// (plain notes were empty already, so the store keeps only what differs)
	if v, ok := ops[0].Payload["html_notes"]; !ok || v != richtext.TaskNotes.FromMarkdown("") {
		t.Fatalf("payload = %+v", ops[0].Payload)
	}
}

// approval_status on a task that is not an approval is refused by the store, and the name
// in the same call is not saved.
func TestUpdateTaskApprovalOnPlainTask(t *testing.T) {
	st := seed(t)
	cs := connect(t, &Server{Store: st})
	before := take(t, st, "t1")
	msg := toolError(call(t, cs, "update_task", map[string]any{"gid": "t1", "name": "新", "approval_status": "approve"}, nil))
	if msg == "" {
		t.Fatal("want an error")
	}
	if after := take(t, st, "t1"); !reflect.DeepEqual(before, after) {
		t.Fatalf("wrote %+v", after.Ops)
	}
	// on an approval task it goes through; "承認" is read as approved
	if err := st.UpsertTasks(context.Background(), []asana.Task{{GID: "ap", Name: "承認", ResourceSubtype: "approval", Workspace: &asana.Ref{GID: "w1"}}}); err != nil {
		t.Fatal(err)
	}
	call(t, cs, "update_task", map[string]any{"gid": "ap", "approval_status": "承認"}, nil)
	ops := updateOps(t, st)
	if len(ops) != 1 || ops[0].Payload["approval_status"] != "approved" {
		t.Fatalf("ops: %+v", ops)
	}
}

// The date is read at the same instant the write is stamped: a clock that crosses midnight
// between two reads does not give a due date from one day and a time from the next.
func TestUpdateTaskReadsTheClockOnce(t *testing.T) {
	st := seed(t)
	day := time.Date(2026, 11, 13, 23, 59, 59, 0, time.UTC)
	n := 0
	s := &Server{Store: st}
	cs := connect(t, s)
	s.Now = func() time.Time { n++; return day.Add(time.Duration(n-1) * time.Second) } // 2nd read is tomorrow
	call(t, cs, "update_task", map[string]any{"gid": "t1", "due_on": "today", "completed": true}, nil)
	ops := updateOps(t, st)
	if len(ops) != 1 {
		t.Fatalf("ops: %+v", ops)
	}
	if got := ops[0].Payload["due_on"]; got != "2026-11-13" {
		t.Fatalf("due_on = %v, want the day of the first read", got)
	}
	if !strings.HasPrefix(ops[0].CreatedAt, "2026-11-13T23:59:59") {
		t.Fatalf("stamped %s: read the clock more than once", ops[0].CreatedAt)
	}
}
