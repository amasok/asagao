package readmodel

import (
	"reflect"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
)

func TestOpLabel(t *testing.T) {
	for _, tc := range []struct {
		state  string
		verify bool
		want   string
	}{
		{outbox.StatePending, false, "未送信"},
		{outbox.StatePending, true, "自動確認中"},
		{outbox.StateFailed, false, "失敗"},
		{outbox.StateFailed, true, "失敗"},
		{outbox.StateConflict, false, "競合"},
		{outbox.StateConflict, true, "競合"},
		{outbox.StateSending, true, "送信中"},
		{"other", false, "other"},
	} {
		if got := OpLabel(outbox.Op{State: tc.state, Verify: tc.verify}); got != tc.want {
			t.Errorf("OpLabel(%s, verify=%v) = %q, want %q", tc.state, tc.verify, got, tc.want)
		}
	}
}

func TestOpNote(t *testing.T) {
	for _, tc := range []struct {
		name          string
		op            outbox.Op
		heading, text string
	}{
		{"failed", outbox.Op{State: outbox.StateFailed, LastError: "400"}, "エラー", "400"},
		{"failed while checked", outbox.Op{State: outbox.StateFailed, Verify: true, LastError: "400"}, "エラー", "400"},
		{"failed without error", outbox.Op{State: outbox.StateFailed}, "エラー", ""},
		{"pending", outbox.Op{State: outbox.StatePending}, "", ""},
		{"pending with a note", outbox.Op{State: outbox.StatePending, LastError: "timeout"}, "状況", "timeout"},
		{"checked", outbox.Op{State: outbox.StatePending, Verify: true}, "状況", "Asana への反映を自動で確認しています。" + VerifyNote},
		{"checked with a note", outbox.Op{State: outbox.StatePending, Verify: true, LastError: "timeout"}, "状況", "timeout。" + VerifyNote},
		{"conflict", outbox.Op{State: outbox.StateConflict, LastError: "changed"}, "状況", "changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, text := OpNote(tc.op)
			if h != tc.heading || text != tc.text {
				t.Errorf("OpNote = %q, %q, want %q, %q", h, text, tc.heading, tc.text)
			}
		})
	}
}

func TestDiscardLine(t *testing.T) {
	for _, tc := range []struct {
		name  string
		d     Discard
		want  string
		reset bool
	}{
		{"with task", Discard{TaskName: "牛乳", Summary: "コメント「x」"}, "「牛乳」: コメント「x」", false},
		{"without task", Discard{Summary: "通知を既読にする"}, "通知を既読にする", false},
		{"deleted in Asana", Discard{TaskName: "t", Summary: "s", Reason: "404"}, "「t」: s", false},
		{"reset", Discard{TaskName: "t", Summary: "s", Reason: ResetReason}, "「t」: s（初期化で破棄）", true},
		{"reset without task", Discard{Summary: "s", Reason: ResetReason}, "s（初期化で破棄）", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DiscardLine(tc.d); got != tc.want {
				t.Errorf("DiscardLine = %q, want %q", got, tc.want)
			}
			if got := IsResetDiscard(tc.d); got != tc.reset {
				t.Errorf("IsResetDiscard = %v, want %v", got, tc.reset)
			}
		})
	}
}

func TestFieldLabel(t *testing.T) {
	for _, tc := range []struct{ k, want string }{
		{"completed", "完了"}, {"name", "名前"}, {"notes", "説明"}, {"due_on", "期日"}, {"assignee", "担当者"},
		{"liked", "いいね"}, {"resource_subtype", "種類"}, {"approval_status", "承認"},
		{"html_notes", ""}, {"", ""}, {"Name", ""},
	} {
		if got := FieldLabel(tc.k); got != tc.want {
			t.Errorf("FieldLabel(%q) = %q, want %q", tc.k, got, tc.want)
		}
	}
}

func TestConflictLines(t *testing.T) {
	for _, tc := range []struct {
		name string
		op   outbox.Op
		want []string
	}{
		{"fields, sorted by key", outbox.Op{Kind: outbox.KindUpdateTask,
			Payload:  map[string]any{"name": "新", "due_on": "2026-01-02", "completed": true},
			Conflict: map[string]any{"name": "旧", "due_on": "2026-01-03"}},
			[]string{"期日: こちら=2026-01-02 / Asana=2026-01-03", "名前: こちら=新 / Asana=旧"}},
		{"no conflict", outbox.Op{Kind: outbox.KindUpdateTask, Payload: map[string]any{"name": "新"}}, []string{}},
		{"project fields", outbox.Op{Kind: outbox.KindUpdateProject,
			Payload:  map[string]any{"start_on": "2026-12-01", "color": "none"},
			Conflict: map[string]any{"start_on": "2026-11-01", "color": "dark-red"}},
			[]string{"色: こちら=none / Asana=dark-red", "開始日: こちら=2026-12-01 / Asana=2026-11-01"}},
		{"custom field by display", outbox.Op{Kind: outbox.KindSetCustomField,
			Payload:  map[string]any{"field_name": "優先度", "value": "o1", "display": "高"},
			Conflict: map[string]any{"value": "o2", "display": "低"}},
			[]string{"優先度: こちら=高 / Asana=低"}},
		{"custom field by value", outbox.Op{Kind: outbox.KindSetCustomField,
			Payload:  map[string]any{"field_name": "点数", "value": 3.0},
			Conflict: map[string]any{"value": nil}},
			[]string{"点数: こちら=3 / Asana=（空）"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConflictLines(tc.op); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ConflictLines = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDescribeChangeAndOp(t *testing.T) {
	long := strings.Repeat("あ", 45)
	for _, tc := range []struct {
		name   string
		op     outbox.Op
		change string
		op1    string // DescribeOp
	}{
		{"create task", outbox.Op{Kind: outbox.KindCreateTask, TaskName: "x", Payload: map[string]any{"name": "牛乳"}}, "タスク「牛乳」の作成", "タスク「牛乳」の作成"},
		{"create subtask", outbox.Op{Kind: outbox.KindCreateTask, Payload: map[string]any{"name": "牛乳", "parent": "t0"}}, "サブタスク「牛乳」の作成", "サブタスク「牛乳」の作成"},
		{"duplicate", outbox.Op{Kind: outbox.KindDuplicateTask, Payload: map[string]any{"name": "牛乳"}}, "複製「牛乳」の作成", "複製「牛乳」の作成"},
		{"comment", outbox.Op{Kind: outbox.KindAddComment, TaskName: "t", Payload: map[string]any{"text": "了解\n  です"}}, "コメント「了解 です」", "t: コメント「了解 です」"},
		{"long comment", outbox.Op{Kind: outbox.KindAddComment, TaskName: "t", Payload: map[string]any{"text": long}}, "コメント「" + strings.Repeat("あ", 40) + "…」", "t: コメント「" + strings.Repeat("あ", 40) + "…」"},
		{"move", outbox.Op{Kind: outbox.KindMoveTask, TaskGID: "t1", Payload: map[string]any{"section_name": "今週"}}, "セクション「今週」への移動", "t1: セクション「今週」への移動"},
		{"add tag", outbox.Op{Kind: outbox.KindAddTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "タグ「急ぎ」の追加", "t: タグ「急ぎ」の追加"},
		{"remove tag", outbox.Op{Kind: outbox.KindRemoveTag, TaskName: "t", Payload: map[string]any{"tag_name": "急ぎ"}}, "タグ「急ぎ」の削除", "t: タグ「急ぎ」の削除"},
		{"remove unnamed tag", outbox.Op{Kind: outbox.KindRemoveTag, TaskName: "t", Payload: map[string]any{}}, "タグの削除", "t: タグの削除"},
		{"add follower", outbox.Op{Kind: outbox.KindAddFollower, TaskName: "t", Payload: map[string]any{"user_name": "花子"}}, "フォロワー 花子 の追加", "t: フォロワー 花子 の追加"},
		{"remove follower", outbox.Op{Kind: outbox.KindRemoveFollower, TaskName: "t", Payload: map[string]any{"user_name": "花子"}}, "フォロワー 花子 の削除", "t: フォロワー 花子 の削除"},
		{"add project", outbox.Op{Kind: outbox.KindAddProject, TaskName: "t", Payload: map[string]any{"project_name": "仕事"}}, "プロジェクト「仕事」への追加", "t: プロジェクト「仕事」への追加"},
		{"remove project", outbox.Op{Kind: outbox.KindRemoveProject, TaskName: "t", Payload: map[string]any{"project_name": "仕事"}}, "プロジェクト「仕事」からの削除", "t: プロジェクト「仕事」からの削除"},
		{"attachment", outbox.Op{Kind: outbox.KindUploadAttachment, TaskName: "t", Payload: map[string]any{"name": "a.pdf"}}, "添付「a.pdf」", "t: 添付「a.pdf」"},
		{"add dependency", outbox.Op{Kind: outbox.KindAddDependency, TaskName: "t", Payload: map[string]any{"dependency": "t2", "dependency_name": "先"}}, "「先」を待つ", "t: 「先」を待つ"},
		{"add dependency by gid", outbox.Op{Kind: outbox.KindAddDependency, TaskName: "t", Payload: map[string]any{"dependency": "t2"}}, "「t2」を待つ", "t: 「t2」を待つ"},
		{"remove dependency", outbox.Op{Kind: outbox.KindRemoveDependency, TaskName: "t", Payload: map[string]any{"dependency_name": "先"}}, "「先」を待つのをやめる", "t: 「先」を待つのをやめる"},
		{"set field", outbox.Op{Kind: outbox.KindSetCustomField, TaskName: "t", Payload: map[string]any{"field_name": "優先度", "value": "o1", "display": "高"}}, "「優先度」を「高」に変更", "t: 「優先度」を「高」に変更"},
		{"clear field", outbox.Op{Kind: outbox.KindSetCustomField, TaskName: "t", Payload: map[string]any{"field_name": "優先度", "value": nil}}, "「優先度」を空にする", "t: 「優先度」を空にする"},
		{"status with title", outbox.Op{Kind: outbox.KindCreateStatus, TaskName: "仕事", Payload: map[string]any{"title": "週報", "status_type": "on_track"}}, "ステータス更新「週報」", "仕事: ステータス更新「週報」"},
		{"status without title", outbox.Op{Kind: outbox.KindCreateStatus, TaskName: "仕事", Payload: map[string]any{"status_type": "at_risk"}}, "ステータス更新（要注意）", "仕事: ステータス更新（要注意）"},
		{"mark read", outbox.Op{Kind: outbox.KindMarkNotification, TaskGID: "n1", Payload: map[string]any{"read": true}}, "通知を既読にする", "n1: 通知を既読にする"},
		{"mark unread", outbox.Op{Kind: outbox.KindMarkNotification, TaskGID: "n1", Payload: map[string]any{"read": false}}, "通知を未読に戻す", "n1: 通知を未読に戻す"},
		{"rename section", outbox.Op{Kind: outbox.KindRenameSection, TaskName: "x", Payload: map[string]any{"name": "来週"}, Base: map[string]any{"name": "今週"}}, "セクション名を「今週」から「来週」に変更", "セクション名を「今週」から「来週」に変更"},
		{"rename section without base", outbox.Op{Kind: outbox.KindRenameSection, Payload: map[string]any{"name": "来週"}}, "セクション名を「来週」に変更", "セクション名を「来週」に変更"},
		{"update, in order", outbox.Op{Kind: outbox.KindUpdateTask, TaskName: "t", Payload: map[string]any{
			"approval_status": "approved", "resource_subtype": "milestone", "liked": true, "notes": "n", "assignee": "u1",
			"due_on": "2026-01-02", "completed": true, "name": "新"}},
			"名前を「新」に変更、完了にする、期日を 2026-01-02 に変更、担当者の変更、説明の変更、いいね、種類を「マイルストーン」に変更、承認を「承認済み」に変更",
			"t: 名前を「新」に変更、完了にする、期日を 2026-01-02 に変更、担当者の変更、説明の変更、いいね、種類を「マイルストーン」に変更、承認を「承認済み」に変更"},
		{"update, clearing", outbox.Op{Kind: outbox.KindUpdateTask, TaskName: "t", Payload: map[string]any{"completed": false, "due_on": "", "assignee": "", "liked": false}},
			"未完了に戻す、期日を削除、担当者を外す、いいねを外す", "t: 未完了に戻す、期日を削除、担当者を外す、いいねを外す"},
		{"update, rich notes only", outbox.Op{Kind: outbox.KindUpdateTask, TaskName: "t", Payload: map[string]any{"html_notes": "<body/>"}}, "説明の変更", "t: 説明の変更"},
		{"update, nothing known", outbox.Op{Kind: outbox.KindUpdateTask, TaskName: "t", Payload: map[string]any{"x": 1}}, "", "t: "},
		{"update project", outbox.Op{Kind: outbox.KindUpdateProject, TaskName: "新居", Payload: map[string]any{
			"color": "light-green", "due_on": "2026-12-31", "start_on": "", "owner": "u1", "html_notes": "<body/>", "notes": "n"}},
			"プロジェクトの説明の変更、オーナーの変更、開始日を外す、期日を 2026-12-31 に変更、色を light-green に変更",
			"新居: プロジェクトの説明の変更、オーナーの変更、開始日を外す、期日を 2026-12-31 に変更、色を light-green に変更"},
		{"update project, rich notes only, no owner", outbox.Op{Kind: outbox.KindUpdateProject, TaskName: "新居", Payload: map[string]any{
			"html_notes": "<body/>", "owner": ""}}, "プロジェクトの説明の変更、オーナーを外す", "新居: プロジェクトの説明の変更、オーナーを外す"},
		{"unknown kind", outbox.Op{Kind: "zap", TaskName: "t"}, "zap", "t: zap"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeChange(tc.op); got != tc.change {
				t.Errorf("DescribeChange = %q, want %q", got, tc.change)
			}
			if got := DescribeOp(tc.op); got != tc.op1 {
				t.Errorf("DescribeOp = %q, want %q", got, tc.op1)
			}
		})
	}
}

func TestTaskBadges(t *testing.T) {
	for _, tc := range []struct {
		name string
		t    TaskSummary
		want string
	}{
		{"plain", TaskSummary{ResourceSubtype: "default_task"}, ""},
		{"milestone", TaskSummary{ResourceSubtype: "milestone"}, "◆"},
		{"approval, no status", TaskSummary{ResourceSubtype: "approval"}, "[承認待ち]"},
		{"approval", TaskSummary{ResourceSubtype: "approval", ApprovalStatus: "approved"}, "[承認済み]"},
		{"waiting", TaskSummary{OpenDependencies: 2}, "[待ち2]"},
		{"waiting but done", TaskSummary{OpenDependencies: 2, Completed: true}, ""},
		{"milestone waiting", TaskSummary{ResourceSubtype: "milestone", OpenDependencies: 1}, "◆ [待ち1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := TaskBadges(tc.t); got != tc.want {
				t.Errorf("TaskBadges = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStatusLabel(t *testing.T) {
	for _, tc := range []struct{ t, want string }{
		{"on_track", "順調"}, {"at_risk", "要注意"}, {"off_track", "遅れ"}, {"on_hold", "保留"}, {"complete", "完了"},
		{"", ""}, {"achieved", "achieved"},
	} {
		if got := StatusLabel(tc.t); got != tc.want {
			t.Errorf("StatusLabel(%q) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestAccountChangedHelp(t *testing.T) {
	got := AccountChangedHelp(3, "asagao reset")
	for _, want := range []string{"未送信の変更: 3 件", "asagao auth login", "（asagao reset。"} {
		if !strings.Contains(got, want) {
			t.Errorf("AccountChangedHelp = %q, want it to hold %q", got, want)
		}
	}
}
