package readmodel

import "testing"

// A custom task type (#59) is shown by its name and status.
func TestCustomTypeBadgeAndLabel(t *testing.T) {
	tk := TaskSummary{ResourceSubtype: "custom", CustomType: &CustomTypeRef{GID: "ct1", Name: "バグ"},
		CustomTypeStatus: &CustomTypeStatusView{GID: "cs1", Name: "対応中"}}
	if got := TaskBadges(tk); got != "[バグ: 対応中]" {
		t.Errorf("badge %q", got)
	}
	if got := KindLabel(tk); got != "バグ" {
		t.Errorf("label %q", got)
	}
	tk.CustomTypeStatus = nil
	if got := TaskBadges(tk); got != "[バグ]" {
		t.Errorf("badge without status %q", got)
	}
	tk.CustomType = nil
	if got := KindLabel(tk); got != "カスタムタイプ" {
		t.Errorf("label of an unknown type %q", got)
	}
	if got := KindLabel(TaskSummary{ResourceSubtype: "milestone"}); got != "マイルストーン" {
		t.Errorf("milestone %q", got)
	}
}
