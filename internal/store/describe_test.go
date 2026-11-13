package store

import (
	"reflect"
	"testing"
)

func TestDescribeOp(t *testing.T) {
	long := "これはとても長いコメントで四十文字を超えるので途中で切られて省略記号が付きます。さらに続きます"
	for _, tc := range []struct {
		name string
		op   Op
		want string
	}{
		{"create", Op{Kind: OpCreateTask, TaskName: "牛乳を買う", Payload: map[string]any{"name": "牛乳を買う"}}, "タスク「牛乳を買う」の作成"},
		{"create subtask", Op{Kind: OpCreateTask, Payload: map[string]any{"name": "子", "parent": "t1"}}, "サブタスク「子」の作成"},
		{"duplicate", Op{Kind: OpDuplicateTask, Payload: map[string]any{"name": "t のコピー"}}, "複製「t のコピー」の作成"},
		{"gid when the name is unknown", Op{Kind: OpMoveTask, TaskGID: "123", Payload: map[string]any{"section_name": "Done"}}, "123: セクション「Done」への移動"},
		{"move", Op{Kind: OpMoveTask, TaskName: "ソファ", Payload: map[string]any{"section_name": "Done"}}, "ソファ: セクション「Done」への移動"},
		{"rename section", Op{Kind: OpRenameSection, TaskName: "新", Base: map[string]any{"name": "旧"}, Payload: map[string]any{"name": "新"}}, "セクション名を「旧」から「新」に変更"},
		{"rename section without base", Op{Kind: OpRenameSection, Payload: map[string]any{"name": "新"}}, "セクション名を「新」に変更"},
		{"status with title", Op{Kind: OpCreateStatus, TaskName: "新居", Payload: map[string]any{"title": "順調", "status_type": "on_track"}}, "新居: ステータス更新「順調」"},
		{"status without title", Op{Kind: OpCreateStatus, TaskName: "新居", Payload: map[string]any{"status_type": "at_risk"}}, "新居: ステータス更新（" + StatusLabel("at_risk") + "）"},
		{"add follower", Op{Kind: OpAddFollower, TaskName: "t", Payload: map[string]any{"user_name": "Partner"}}, "t: フォロワー Partner の追加"},
		{"remove follower", Op{Kind: OpRemoveFollower, TaskName: "t", Payload: map[string]any{"user_name": "Partner"}}, "t: フォロワー Partner の削除"},
		{"add project", Op{Kind: OpAddProject, TaskName: "t", Payload: map[string]any{"project_name": "家計"}}, "t: プロジェクト「家計」への追加"},
		{"remove project", Op{Kind: OpRemoveProject, TaskName: "t", Payload: map[string]any{"project_name": "家計"}}, "t: プロジェクト「家計」からの削除"},
		{"upload", Op{Kind: OpUploadAttachment, TaskName: "t", Payload: map[string]any{"name": "a.pdf"}}, "t: 添付「a.pdf」"},
		{"add tag", Op{Kind: OpAddTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "t: タグ「急ぎ」の追加"},
		{"remove tag", Op{Kind: OpRemoveTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "t: タグ「急ぎ」の削除"},
		{"short comment", Op{Kind: OpAddComment, TaskName: "t", Payload: map[string]any{"text": "了解"}}, "t: コメント「了解」"},
		{"long comment is cut at 40 runes, one line", Op{Kind: OpAddComment, TaskName: "t", Payload: map[string]any{"text": "改行\n" + long}},
			"t: コメント「" + string([]rune("改行 " + long)[:40]) + "…」"},
		{"update: fields in a fixed order", Op{Kind: OpUpdateTask, TaskName: "t", Payload: map[string]any{
			"assignee": "u2", "due_on": "2026-12-01", "notes": "x", "name": "新", "completed": true, "liked": true}},
			"t: 名前を「新」に変更、完了にする、期日を 2026-12-01 に変更、担当者の変更、説明の変更、いいね"},
		{"update: reopen and clear due date", Op{Kind: OpUpdateTask, TaskName: "t", Payload: map[string]any{"completed": false, "due_on": ""}},
			"t: 未完了に戻す、期日を削除"},
		{"update: unlike and unassign", Op{Kind: OpUpdateTask, TaskName: "t", Payload: map[string]any{"liked": false, "assignee": ""}},
			"t: 担当者を外す、いいねを外す"},
		{"update: rich notes only", Op{Kind: OpUpdateTask, TaskName: "t", Payload: map[string]any{"html_notes": "<body>x</body>"}}, "t: 説明の変更"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeOp(tc.op); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestOpLabels(t *testing.T) {
	for _, tc := range []struct {
		name    string
		op      Op
		label   string
		heading string
		note    string
	}{
		{"pending", Op{State: OpPending}, "未送信", "", ""},
		{"being checked", Op{State: OpPending, Verify: true, LastError: "Asana への反映を待っています"},
			"自動確認中", "状況", "Asana への反映を待っています。" + VerifyNote},
		{"being checked, no note from push", Op{State: OpPending, Verify: true},
			"自動確認中", "状況", "Asana への反映を自動で確認しています。" + VerifyNote},
		{"sending", Op{State: OpSending}, "送信中", "", ""},
		{"sending while checked", Op{State: OpSending, Verify: true}, "送信中", "状況", "Asana への反映を自動で確認しています。" + VerifyNote},
		{"conflict", Op{State: OpConflict}, "競合", "", ""},
		{"failed", Op{State: OpFailed, LastError: "boom"}, "失敗", "エラー", "boom"},
		{"unknown state", Op{State: "weird"}, "weird", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := OpLabel(tc.op); got != tc.label {
				t.Errorf("OpLabel = %q, want %q", got, tc.label)
			}
			if h, n := OpNote(tc.op); h != tc.heading || n != tc.note {
				t.Errorf("OpNote = %q, %q; want %q, %q", h, n, tc.heading, tc.note)
			}
		})
	}
	if got := OpStateLabel(OpSending); got != "送信中" {
		t.Errorf("OpStateLabel(sending) = %q", got)
	}
}

func TestDiscardLine(t *testing.T) {
	if got := DiscardLine(Discard{TaskName: "ソファを買う", Summary: "名前を「x」に変更"}); got != "「ソファを買う」: 名前を「x」に変更" {
		t.Errorf("with a task name: %q", got)
	}
	if got := DiscardLine(Discard{Summary: "セクション名を「x」に変更"}); got != "セクション名を「x」に変更" {
		t.Errorf("without a task name: %q", got)
	}
	if got := DiscardLine(Discard{TaskName: "t", Summary: "コメント「x」", Reason: ResetReason}); got != "「t」: コメント「x」（初期化で破棄）" {
		t.Errorf("deleted by a reset: %q", got)
	}
}

func TestDiscardDetails(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    Discard
		want []DiscardDetail
	}{
		{"comment in full", Discard{Kind: OpAddComment, Payload: map[string]any{"text": "長い\nコメント", "story_gid": "local:1"}},
			[]DiscardDetail{{"コメント", "長い\nコメント"}}},
		{"created task", Discard{Kind: OpCreateTask, Payload: map[string]any{"name": "新規", "notes": "説明文", "html_notes": "<body>説明文</body>"}},
			[]DiscardDetail{{"名前", "新規"}, {"説明（Markdown）", "説明文"}}},
		{"plain description", Discard{Kind: OpUpdateTask, Payload: map[string]any{"notes": "説明文"}},
			[]DiscardDetail{{"説明", "説明文"}}},
		{"update without text fields", Discard{Kind: OpUpdateTask, Payload: map[string]any{"completed": true}}, nil},
		{"status", Discard{Kind: OpCreateStatus, Payload: map[string]any{"title": "順調", "text": "本文"}},
			[]DiscardDetail{{"タイトル", "順調"}, {"本文", "本文"}}},
		{"attachment copy", Discard{Kind: OpUploadAttachment, Payload: map[string]any{"name": "a.pdf", "path": "/cache/a.pdf"}},
			[]DiscardDetail{{"ファイルのコピー", "/cache/a.pdf"}}},
		{"tag", Discard{Kind: OpAddTag, Payload: map[string]any{"tag_name": "急ぎ"}}, nil},
		{"rich comment as Markdown", Discard{Kind: OpAddComment, Payload: map[string]any{"text": "大事 です", "html_text": "<body><strong>大事</strong> です</body>"}},
			[]DiscardDetail{{"コメント（Markdown）", "**大事** です"}}},
		{"rich description as Markdown", Discard{Kind: OpUpdateTask, Payload: map[string]any{"notes": "見出し", "html_notes": "<body><h1>見出し</h1></body>"}},
			[]DiscardDetail{{"説明（Markdown）", "# 見出し"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiscardDetails(tc.d); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
