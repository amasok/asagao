package app

import (
	"context"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// Attack tests for naming custom task types (#59).

func TestAttackResolveKindOddInputs(t *testing.T) {
	ctx := context.Background()
	str := func(s string) *string { return &s }
	custom := bugKind
	custom.Subtype, custom.Type = "custom", &readmodel.CustomTypeRef{GID: "ct1", Name: "バグ"}
	for _, tc := range []struct {
		name string
		kind readmodel.TaskKind
		in   UpdateTaskInput
	}{
		{"a type and another in resource_subtype", bugKind, UpdateTaskInput{Subtype: str("バグ"), CustomType: str("Request")}},
		{"the word custom alone", bugKind, UpdateTaskInput{Subtype: str("custom")}},
		{"empty subtype", bugKind, UpdateTaskInput{Subtype: str("")}},
		{"status gid of another type by name", custom, UpdateTaskInput{CustomTypeStatus: str("cr1")}},
		{"disabled status name", custom, UpdateTaskInput{CustomTypeStatus: str("古い")}},
		{"empty status", custom, UpdateTaskInput{CustomTypeStatus: str("  ")}},
		{"control characters", bugKind, UpdateTaskInput{CustomType: str("バ\x1b[2Jグ")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks, _ := kindTasks(tc.kind)
			u, err := tasks.ResolveUpdateFor(ctx, "9", tc.in)
			if err == nil {
				t.Fatalf("accepted: %+v", u)
			}
		})
	}
}

// Two types with the same name resolve to the first by name, but a gid always wins.
func TestAttackSameNameTypes(t *testing.T) {
	k := readmodel.TaskKind{Subtype: "default_task", Choices: []readmodel.CustomTypeView{
		{GID: "a", Name: "同じ"}, {GID: "b", Name: "同じ"}}}
	tasks, _ := kindTasks(k)
	b := "b"
	u, err := tasks.ResolveUpdateFor(context.Background(), "9", UpdateTaskInput{CustomType: &b})
	if err != nil || *u.CustomType != "b" {
		t.Fatalf("%+v %v", u, err)
	}
}

// A custom task turned into a plain one with a status in the same call is refused, and
// a custom type given with a custom task whose own type is not offered anymore works.
func TestAttackOwnTypeNotOffered(t *testing.T) {
	k := readmodel.TaskKind{Subtype: "custom", Type: &readmodel.CustomTypeRef{GID: "x", Name: "X"},
		Choices: []readmodel.CustomTypeView{{GID: "x", Name: "X", Statuses: []readmodel.CustomTypeStatusView{{GID: "xs", Name: "S"}}}}}
	tasks, _ := kindTasks(k)
	s := "S"
	u, err := tasks.ResolveUpdateFor(context.Background(), "9", UpdateTaskInput{CustomTypeStatus: &s})
	if err != nil || *u.CustomTypeStatus != "xs" {
		t.Fatalf("%+v %v", u, err)
	}
}
