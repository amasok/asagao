package readmodel

import (
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// #60: a brief change says whether it creates, changes or deletes the brief.
func TestDescribeBriefChange(t *testing.T) {
	for _, tc := range []struct {
		payload, base map[string]any
		want          string
	}{
		{map[string]any{"title": "目的"}, map[string]any{"state": BriefNone}, "ブリーフ「目的」の作成"},
		{map[string]any{"title": "目的"}, map[string]any{"state": ""}, "ブリーフ「目的」の作成"},
		{map[string]any{"title": "目的"}, map[string]any{"state": BriefAvailable}, "ブリーフ「目的」の変更"},
		{map[string]any{"delete": true}, map[string]any{"state": BriefAvailable}, "ブリーフの削除"},
	} {
		op := outbox.Op{Kind: outbox.KindUpdateProjectBrief, TaskName: "新居", Payload: tc.payload, Base: tc.base}
		if got := DescribeOp(op); got != "新居: "+tc.want {
			t.Errorf("%v: %q, want %q", tc.payload, got, tc.want)
		}
	}
	if f := PlanFeatureOf(outbox.KindUpdateProjectBrief); f != PlanProjectBrief {
		t.Fatalf("feature %q", f)
	}
}

// A conflict deleted in Asana says so.
func TestBriefConflictDeletedThere(t *testing.T) {
	op := outbox.Op{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"title": "t", "text": "x"},
		Conflict: map[string]any{"state": BriefNone}}
	if lines := ConflictLines(op); len(lines) != 1 || lines[0] != "ブリーフ: こちら=t / Asana=（ブリーフがありません。削除されています）" {
		t.Fatalf("lines = %q", lines)
	}
}
