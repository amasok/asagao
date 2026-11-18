package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// Custom task types (#59): names are read against the types the task can be given.

var bugKind = readmodel.TaskKind{Subtype: "default_task", Choices: []readmodel.CustomTypeView{
	{GID: "ct1", Name: "バグ", Statuses: []readmodel.CustomTypeStatusView{{GID: "cs1", Name: "対応中"}, {GID: "cs2", Name: "完了", Complete: true}}},
	{GID: "ct2", Name: "Request", Statuses: []readmodel.CustomTypeStatusView{{GID: "cr1", Name: "受付"}}},
}}

func kindTasks(k readmodel.TaskKind) (*Tasks, *updateStore) {
	st := &updateStore{fakeStore: &fakeStore{kind: k}}
	return &Tasks{Store: st, Clock: func() time.Time { return time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC) }}, st
}

func TestResolveCustomTypeByName(t *testing.T) {
	tasks, _ := kindTasks(bugKind)
	ctx := context.Background()
	for _, arg := range []string{"バグ", "ct1"} {
		u, err := tasks.ResolveUpdateFor(ctx, "9", UpdateTaskInput{Subtype: &arg})
		if err != nil || u.Subtype == nil || *u.Subtype != "custom" || u.CustomType == nil || *u.CustomType != "ct1" || u.CustomTypeName != "バグ" {
			t.Fatalf("Subtype %q: %+v %v", arg, u, err)
		}
	}
	req := "request" // ignoring case
	u, err := tasks.ResolveUpdateFor(ctx, "9", UpdateTaskInput{CustomType: &req})
	if err != nil || *u.CustomType != "ct2" || *u.Subtype != "custom" {
		t.Fatalf("CustomType: %+v %v", u, err)
	}
	// A status of the new type.
	st := "受付"
	if u, err = tasks.ResolveUpdateFor(ctx, "9", UpdateTaskInput{CustomType: &req, CustomTypeStatus: &st}); err != nil || *u.CustomTypeStatus != "cr1" || u.CustomTypeStatusName != "受付" {
		t.Fatalf("status: %+v %v", u, err)
	}
	// The built-in types still need no task.
	ms := "マイルストーン"
	if u, err := tasks.ResolveUpdate(ctx, UpdateTaskInput{Subtype: &ms}); err != nil || *u.Subtype != "milestone" {
		t.Fatalf("milestone: %+v %v", u, err)
	}
}

func TestResolveCustomTypeErrors(t *testing.T) {
	ctx := context.Background()
	custom := bugKind
	custom.Subtype, custom.Type = "custom", &readmodel.CustomTypeRef{GID: "ct1", Name: "バグ"}
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name  string
		kind  readmodel.TaskKind
		in    UpdateTaskInput
		field string
		msg   string
	}{
		{"unknown type name", bugKind, UpdateTaskInput{Subtype: str("機能")}, "resource_subtype", "バグ / Request"},
		{"unknown custom type", bugKind, UpdateTaskInput{CustomType: str("機能")}, "custom_type", "バグ / Request のどれか"},
		{"no types at all", readmodel.TaskKind{Subtype: "default_task"}, UpdateTaskInput{CustomType: str("バグ")}, "custom_type", "同期すると"},
		{"status on a plain task", bugKind, UpdateTaskInput{CustomTypeStatus: str("対応中")}, "custom_type_status_option", "カスタムタイプのタスクではない"},
		{"status of another type", custom, UpdateTaskInput{CustomTypeStatus: str("受付")}, "custom_type_status_option", "対応中 / 完了"},
		{"status and leaving custom", custom, UpdateTaskInput{Subtype: str("task"), CustomTypeStatus: str("完了")}, "custom_type_status_option", "カスタムタイプのタスクではない"},
		{"type with another kind", bugKind, UpdateTaskInput{Subtype: str("milestone"), CustomType: str("バグ")}, "custom_type", "同時に"},
		{"empty type", bugKind, UpdateTaskInput{CustomType: str(" ")}, "custom_type", ""},
		{"two different types", bugKind, UpdateTaskInput{Subtype: str("バグ"), CustomType: str("Request")}, "custom_type", "違う種類"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks, _ := kindTasks(tc.kind)
			_, err := tasks.ResolveUpdateFor(ctx, "9", tc.in)
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field || !strings.Contains(fe.Err.Error(), tc.msg) {
				t.Fatalf("err = %v, want %s: …%s…", err, tc.field, tc.msg)
			}
		})
	}
	// Without a task, a custom type cannot be read.
	tasks, _ := kindTasks(bugKind)
	if _, err := tasks.ResolveUpdate(ctx, UpdateTaskInput{CustomType: str("バグ")}); err == nil {
		t.Fatal("ResolveUpdate took a custom type")
	}
}

// Update sends the type and its status as the store takes them.
func TestUpdateSendsCustomType(t *testing.T) {
	tasks, st := kindTasks(bugKind)
	name, status := "バグ", "完了"
	if _, err := tasks.Update(context.Background(), "9", UpdateTaskInput{Subtype: &name, CustomTypeStatus: &status}); err != nil {
		t.Fatal(err)
	}
	if len(st.changes) != 1 || st.changes[0]["resource_subtype"] != "custom" || st.changes[0]["custom_type"] != "ct1" || st.changes[0]["custom_type_status_option"] != "cs2" {
		t.Fatalf("changes %v", st.changes)
	}
}
