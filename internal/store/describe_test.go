package store

import "testing"

func TestDescribeOp(t *testing.T) {
	long := "これはとても長いコメントで三十文字を超えるので途中で切られて省略記号が付きます"
	for _, tc := range []struct {
		name string
		op   Op
		want string
	}{
		{"create", Op{Kind: OpCreateTask, TaskName: "牛乳を買う"}, "作成: 牛乳を買う"},
		{"gid when the name is unknown", Op{Kind: OpCreateTask, TaskGID: "local:ab"}, "作成: local:ab"},
		{"move", Op{Kind: OpMoveTask, TaskName: "ソファ", Payload: map[string]any{"section_name": "Done"}}, "移動: ソファ → Done"},
		{"rename section", Op{Kind: OpRenameSection, Base: map[string]any{"name": "旧"}, Payload: map[string]any{"name": "新"}}, "セクション名の変更: 旧 → 新"},
		{"status", Op{Kind: OpCreateStatus, TaskName: "新居", Payload: map[string]any{"status_type": "at_risk"}}, "ステータス更新: 新居（" + StatusLabel("at_risk") + "）"},
		{"add follower", Op{Kind: OpAddFollower, TaskName: "t", Payload: map[string]any{"user_name": "Partner"}}, "フォロワーを追加: t Partner"},
		{"remove follower", Op{Kind: OpRemoveFollower, TaskName: "t", Payload: map[string]any{"user_name": "Partner"}}, "フォロワーを外す: t Partner"},
		{"add project", Op{Kind: OpAddProject, TaskName: "t", Payload: map[string]any{"project_name": "家計"}}, "プロジェクトに追加: t → 家計"},
		{"remove project", Op{Kind: OpRemoveProject, TaskName: "t", Payload: map[string]any{"project_name": "家計"}}, "プロジェクトから外す: t ← 家計"},
		{"duplicate", Op{Kind: OpDuplicateTask, TaskName: "t のコピー"}, "複製: t のコピー"},
		{"upload", Op{Kind: OpUploadAttachment, TaskName: "t", Payload: map[string]any{"name": "a.pdf"}}, "ファイルを添付: t ← a.pdf"},
		{"add tag", Op{Kind: OpAddTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "タグを付ける: t #急ぎ"},
		{"remove tag", Op{Kind: OpRemoveTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "タグを外す: t #急ぎ"},
		{"short comment", Op{Kind: OpAddComment, TaskName: "t", Payload: map[string]any{"text": "了解"}}, "コメント: t「了解」"},
		{"long comment is cut at 30 runes", Op{Kind: OpAddComment, TaskName: "t", Payload: map[string]any{"text": long}},
			"コメント: t「" + string([]rune(long)[:30]) + "…」"},
		{"update: all fields in a fixed order", Op{Kind: OpUpdateTask, TaskName: "t", Payload: map[string]any{
			"assignee": "u2", "due_on": "2026-12-01", "notes": "x", "name": "新", "completed": true}},
			"t: 完了にする、名前→新、説明を変更、期日→2026-12-01、担当者を変更"},
		{"update: reopen and clear due date", Op{Kind: OpUpdateTask, TaskName: "t", Payload: map[string]any{"completed": false, "due_on": ""}},
			"t: 未完了に戻す、期日を削除"},
		{"update: unknown keys are not listed", Op{Kind: OpUpdateTask, TaskName: "t", Payload: map[string]any{"liked": true}}, "t: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeOp(tc.op); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}
