package format

import (
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// Every outbox kind is described in words, not by its stored name, and nothing else is.
func TestEveryKindIsDescribed(t *testing.T) {
	kinds := outbox.AllKinds()
	for _, k := range kinds {
		if describers[k] == nil {
			t.Errorf("%s is not described", k)
			continue
		}
		if got := DescribeChange(outbox.Op{Kind: k, Payload: map[string]any{}}); got == string(k) {
			t.Errorf("%s is described as %q", k, got)
		}
	}
	if len(describers) != len(kinds) {
		t.Errorf("%d descriptions for %d kinds", len(describers), len(kinds))
	}
	if got := DescribeChange(outbox.Op{Kind: "from_the_future"}); got != "from_the_future" {
		t.Errorf("an unknown kind is described as %q", got)
	}
}

// An entry is prefixed with its task unless its description names what it is about.
func TestDescribeOpPrefixesTasksOnly(t *testing.T) {
	for _, k := range outbox.AllKinds() {
		op := outbox.Op{Kind: k, TaskName: "タスクA", Payload: map[string]any{}}
		got, what := DescribeOp(op), DescribeChange(op)
		prefixed := got == "タスクA: "+what
		switch outbox.SubjectOf(k) {
		case outbox.SubjectNewTask, outbox.SubjectSection, outbox.SubjectTag, outbox.SubjectEnumOption:
			if got != what {
				t.Errorf("%s: %q is prefixed", k, got)
			}
		default:
			if !prefixed {
				t.Errorf("%s: %q is not prefixed", k, got)
			}
		}
	}
}
