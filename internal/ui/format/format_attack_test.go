package format

import (
	"math"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

func TestFormatEmptyPayloadsAndBoundaries(t *testing.T) {
	// All outbox kinds with completely empty/nil maps must never panic.
	allKinds := []outbox.Kind{
		"", "unknown_kind",
		outbox.KindCreateTask, outbox.KindDuplicateTask, outbox.KindAddComment,
		outbox.KindMoveTask, outbox.KindDeleteTask, outbox.KindSetParent,
		outbox.KindAddTag, outbox.KindRemoveTag, outbox.KindAddFollower, outbox.KindRemoveFollower,
		outbox.KindAddProject, outbox.KindRemoveProject, outbox.KindUploadAttachment,
		outbox.KindUpdateStory, outbox.KindDeleteStory, outbox.KindDeleteAttachment,
		outbox.KindAddTimeEntry, outbox.KindUpdateTimeEntry, outbox.KindDeleteTimeEntry,
		outbox.KindAddDependency, outbox.KindRemoveDependency, outbox.KindSetCustomField,
		outbox.KindCreateStatus, outbox.KindMarkNotification, outbox.KindCreateSection,
		outbox.KindDeleteSection, outbox.KindInsertSection, outbox.KindUpdateTag,
		outbox.KindDeleteTag, outbox.KindMoveMySection, outbox.KindCreateEnumOption,
		outbox.KindAddFieldSetting, outbox.KindRemoveFieldSetting, outbox.KindUpdateProjectBrief,
		outbox.KindUpdateProject, outbox.KindRenameSection, outbox.KindUpdateTask,
	}
	for _, kind := range allKinds {
		op := outbox.Op{Kind: kind}
		_ = DescribeOp(op)
		_ = DescribeChange(op)
		_ = ConflictLines(op)
		_ = OpLabel(op)
		_, _ = OpNote(op)
	}

	// Wrongly typed / extreme payload values must not panic.
	oddOps := []outbox.Op{
		{Kind: outbox.KindAddTimeEntry, TaskName: "t", Payload: map[string]any{"duration_minutes": math.NaN(), "entered_on": 123}},
		{Kind: outbox.KindUpdateTimeEntry, TaskName: "t", Payload: map[string]any{"duration_minutes": math.Inf(1)}},
		{Kind: outbox.KindDeleteTimeEntry, TaskName: "t", Payload: map[string]any{"duration_minutes": -5}},
		{Kind: outbox.KindUpdateTask, TaskName: "t", Payload: map[string]any{"name": 123, "completed": "yes", "due_on": nil, "start_on": []int{1}, "liked": "no"}},
		{Kind: outbox.KindUpdateProject, TaskName: "p", Payload: map[string]any{"owner": 99, "notes": true, "color": nil}},
		{Kind: outbox.KindAddComment, TaskName: "t", Payload: map[string]any{"text": "\xff\xfe" + strings.Repeat("あ", 100)}},
		{Kind: outbox.KindSetCustomField, TaskName: "t", Payload: map[string]any{"field_name": nil, "value": []string{"a"}}, Conflict: map[string]any{"value": nil}},
		{Kind: outbox.KindUpdateProjectBrief, TaskName: "p", Payload: map[string]any{"title": 123, "text": false}, Conflict: map[string]any{"state": readmodel.BriefAvailable, "title": nil}},
	}
	for _, op := range oddOps {
		_ = DescribeOp(op)
		_ = ConflictLines(op)
	}

	// TaskBadges & KindLabel & ReactionsLine boundary inputs.
	if got := TaskBadges(readmodel.TaskSummary{OpenDependencies: -3}); got != "" {
		t.Errorf("negative OpenDependencies: got %q, want empty", got)
	}
	if got := TaskBadges(readmodel.TaskSummary{ResourceSubtype: "custom"}); got != "[カスタムタイプ]" {
		t.Errorf("custom with nil refs: got %q, want [カスタムタイプ]", got)
	}
	if got := TaskBadges(readmodel.TaskSummary{
		ResourceSubtype:  "custom",
		CustomType:       &readmodel.CustomTypeRef{Name: "課題"},
		CustomTypeStatus: &readmodel.CustomTypeStatusView{Name: ""},
	}); got != "[課題]" {
		t.Errorf("custom with empty status name: got %q, want [課題]", got)
	}
	if got := TaskBadges(readmodel.TaskSummary{ResourceSubtype: "approval", ApprovalStatus: "unknown_status"}); got != "[unknown_status]" {
		t.Errorf("unknown approval status: got %q", got)
	}
	if got := ReactionsLine([]readmodel.Reaction{{EmojiBase: "👍", Count: -1}, {EmojiBase: "", Count: 1}}); got != " 1" {
		t.Errorf("ReactionsLine edge: got %q", got)
	}
}

// TestFormatControlCharactersSanitized verifies that terminal control sequences in
// TaskName, payloads, conflicts, and custom type badges cannot survive formatting.
func TestFormatControlCharactersSanitized(t *testing.T) {
	attacks := []struct {
		name string
		raw  string
	}{
		{"ESC CSI", "\x1b[31mred\x1b[0m"},
		{"OSC 52", "\x1b]52;c;YWJj\x07"},
		{"C1 CSI", "\u009b31mtext"},
		{"NUL", "a\x00b"},
		{"DEL", "a\x7fb"},
		{"CR", "safe\roverwrite"},
	}

	for _, a := range attacks {
		t.Run("DescribeOp/"+a.name, func(t *testing.T) {
			ops := []outbox.Op{
				{Kind: outbox.KindUpdateTask, TaskName: a.raw, Payload: map[string]any{"name": a.raw, "due_on": a.raw}},
				{Kind: outbox.KindAddComment, TaskName: "task", Payload: map[string]any{"text": a.raw}},
				{Kind: outbox.KindCreateTask, Payload: map[string]any{"name": a.raw}},
				{Kind: outbox.KindSetCustomField, TaskName: a.raw, Payload: map[string]any{"field_name": a.raw, "value": "v", "display": a.raw}},
				{Kind: outbox.KindUpdateProjectBrief, TaskName: a.raw, Payload: map[string]any{"title": a.raw}, Base: map[string]any{"state": readmodel.BriefNone}},
			}
			for _, op := range ops {
				got := DescribeOp(op)
				if !termsafe.Safe(got) {
					t.Errorf("DescribeOp(%s) left unsafe control characters: %q", op.Kind, got)
				}
			}
		})

		t.Run("ConflictLines/"+a.name, func(t *testing.T) {
			ops := []outbox.Op{
				{Kind: outbox.KindUpdateTask, Payload: map[string]any{"name": a.raw}, Conflict: map[string]any{"name": a.raw}},
				{Kind: outbox.KindSetCustomField, Payload: map[string]any{"field_name": a.raw, "value": "1", "display": a.raw}, Conflict: map[string]any{"value": "2", "display": a.raw}},
				{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"title": a.raw, "text": a.raw}, Conflict: map[string]any{"state": readmodel.BriefAvailable, "title": a.raw + "_s", "text": a.raw + "_t"}},
			}
			for _, op := range ops {
				for _, line := range ConflictLines(op) {
					if !termsafe.Safe(line) {
						t.Errorf("ConflictLines(%s) left unsafe control characters: %q", op.Kind, line)
					}
				}
			}
		})

		t.Run("TaskBadges/"+a.name, func(t *testing.T) {
			tk := readmodel.TaskSummary{
				ResourceSubtype:  "custom",
				CustomType:       &readmodel.CustomTypeRef{GID: "ct1", Name: a.raw},
				CustomTypeStatus: &readmodel.CustomTypeStatusView{GID: "cs1", Name: a.raw},
			}
			got := TaskBadges(tk)
			if !termsafe.Safe(got) {
				t.Errorf("TaskBadges left unsafe control characters: %q", got)
			}
		})
	}
}
