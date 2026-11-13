package cli

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/store"
)

func TestDescribeOp(t *testing.T) {
	long := "これはとても長いコメントで三十文字を超えるので途中で切られて省略記号が付きます"
	for _, tc := range []struct {
		name string
		op   store.Op
		want string
	}{
		{"create", store.Op{Kind: store.OpCreateTask, TaskName: "牛乳を買う"}, "作成: 牛乳を買う"},
		{"gid when the name is unknown", store.Op{Kind: store.OpCreateTask, TaskGID: "local:ab"}, "作成: local:ab"},
		{"move", store.Op{Kind: store.OpMoveTask, TaskName: "ソファ", Payload: map[string]any{"section_name": "Done"}}, "移動: ソファ → Done"},
		{"rename section", store.Op{Kind: store.OpRenameSection, Base: map[string]any{"name": "旧"}, Payload: map[string]any{"name": "新"}}, "セクション名の変更: 旧 → 新"},
		{"status", store.Op{Kind: store.OpCreateStatus, TaskName: "新居", Payload: map[string]any{"status_type": "at_risk"}}, "ステータス更新: 新居（" + store.StatusLabel("at_risk") + "）"},
		{"add follower", store.Op{Kind: store.OpAddFollower, TaskName: "t", Payload: map[string]any{"user_name": "Partner"}}, "フォロワーを追加: t Partner"},
		{"remove follower", store.Op{Kind: store.OpRemoveFollower, TaskName: "t", Payload: map[string]any{"user_name": "Partner"}}, "フォロワーを外す: t Partner"},
		{"add project", store.Op{Kind: store.OpAddProject, TaskName: "t", Payload: map[string]any{"project_name": "家計"}}, "プロジェクトに追加: t → 家計"},
		{"remove project", store.Op{Kind: store.OpRemoveProject, TaskName: "t", Payload: map[string]any{"project_name": "家計"}}, "プロジェクトから外す: t ← 家計"},
		{"duplicate", store.Op{Kind: store.OpDuplicateTask, TaskName: "t のコピー"}, "複製: t のコピー"},
		{"upload", store.Op{Kind: store.OpUploadAttachment, TaskName: "t", Payload: map[string]any{"name": "a.pdf"}}, "ファイルを添付: t ← a.pdf"},
		{"add tag", store.Op{Kind: store.OpAddTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "タグを付ける: t #急ぎ"},
		{"remove tag", store.Op{Kind: store.OpRemoveTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "タグを外す: t #急ぎ"},
		{"short comment", store.Op{Kind: store.OpAddComment, TaskName: "t", Payload: map[string]any{"text": "了解"}}, "コメント: t「了解」"},
		{"long comment is cut at 30 runes", store.Op{Kind: store.OpAddComment, TaskName: "t", Payload: map[string]any{"text": long}},
			"コメント: t「" + string([]rune(long)[:30]) + "…」"},
		{"update: all fields in a fixed order", store.Op{Kind: store.OpUpdateTask, TaskName: "t", Payload: map[string]any{
			"assignee": "u2", "due_on": "2026-12-01", "notes": "x", "name": "新", "completed": true}},
			"t: 完了にする、名前→新、説明を変更、期日→2026-12-01、担当者を変更"},
		{"update: reopen and clear due date", store.Op{Kind: store.OpUpdateTask, TaskName: "t", Payload: map[string]any{"completed": false, "due_on": ""}},
			"t: 未完了に戻す、期日を削除"},
		{"update: unknown keys are not listed", store.Op{Kind: store.OpUpdateTask, TaskName: "t", Payload: map[string]any{"liked": true}}, "t: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeOp(tc.op); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestMembershipLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms   []store.MembershipView
		want string
	}{
		{"none", nil, ""},
		{"project and section", []store.MembershipView{{ProjectName: "新居", SectionName: "家具"}}, "新居 / 家具"},
		{"no section", []store.MembershipView{{ProjectName: "新居"}}, "新居"},
		{"placeholder sections are hidden", []store.MembershipView{
			{ProjectName: "A", SectionName: "(no section)"}, {ProjectName: "B", SectionName: "Untitled section"}}, "A, B"},
		{"several projects", []store.MembershipView{
			{ProjectName: "新居", SectionName: "家具"}, {ProjectName: "家計"}}, "新居 / 家具, 家計"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := membershipLabel(tc.ms); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The filter logic itself is tested in store (TestBuildFilter); this checks only how
// the CLI flags map onto it.
func TestListFlagsSpec(t *testing.T) {
	open := false
	for _, tc := range []struct {
		name string
		args []string
		want store.FilterSpec
	}{
		{"defaults", nil, store.FilterSpec{DefaultAssignee: "me", DefaultCompleted: &open}},
		{"explicit assignee", []string{"--assignee", "Partner"}, store.FilterSpec{Assignee: "Partner", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"empty assignee means everyone", []string{"--assignee="}, store.FilterSpec{Assignee: "any", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"--all wins over --completed", []string{"--completed", "--all"}, store.FilterSpec{Completion: "all", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"--completed", []string{"--completed"}, store.FilterSpec{Completion: "completed", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"the rest pass through", []string{"--project", "新居", "--section", "家具", "--tag", "急ぎ", "--due-before", "+7d",
			"--due-after", "today", "--overdue", "--modified-within", "3", "--limit", "5"},
			store.FilterSpec{DefaultAssignee: "me", DefaultCompleted: &open, Project: "新居", Section: "家具", Tag: "急ぎ",
				DueBefore: "+7d", DueAfter: "today", Overdue: true, ModifiedWithinDays: 3, Limit: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f listFlags
			cmd := &cobra.Command{Use: "x"}
			f.register(cmd, "me")
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := f.spec(cmd, "me", &open); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
