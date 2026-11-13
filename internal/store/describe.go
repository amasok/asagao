package store

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Human-readable (Japanese) descriptions of outbox entries, shared by the TUI and the CLI.

// OpStateLabel is the label of an outbox entry state.
func OpStateLabel(state string) string {
	switch state {
	case OpPending:
		return "未送信"
	case OpConflict:
		return "競合"
	case OpFailed:
		return "失敗"
	case OpSending:
		return "送信中"
	}
	return state
}

// VerifyNote tells the user that an entry being checked against Asana needs nothing from them.
const VerifyNote = "操作は不要です。次の同期で自動的に確認します"

// OpLabel is OpStateLabel, except that a queued entry Asana may already have applied
// (Verify: Push checks Asana by itself before sending it again) is "自動確認中".
func OpLabel(op Op) string {
	if op.State == OpPending && op.Verify {
		return "自動確認中"
	}
	return OpStateLabel(op.State)
}

// OpNote is the note to show under an entry ("" for none): heading is "エラー" for a
// failed entry and "状況" otherwise. An entry being checked always gets a note saying
// that the user does not need to do anything.
func OpNote(op Op) (heading, text string) {
	if op.State == OpFailed {
		return "エラー", op.LastError
	}
	text = op.LastError
	if op.Verify {
		if text == "" {
			text = "Asana への反映を自動で確認しています"
		}
		text += "。" + VerifyNote
	}
	if text == "" {
		return "", ""
	}
	return "状況", text
}

// DiscardNotice heads the one-time notice about changes dropped because what they changed
// (a task, or its section, project, tag...) was deleted in Asana (see PendingDiscards).
const DiscardNotice = "Asana で削除されたものへの変更を破棄しました"

// DiscardLine describes one dropped change: 「task」: what it changed.
func DiscardLine(d Discard) string {
	if d.TaskName == "" {
		return d.Summary
	}
	return "「" + d.TaskName + "」: " + d.Summary
}

// FieldLabel is the label of an update_task field ("" for unknown keys).
func FieldLabel(k string) string {
	return map[string]string{"completed": "完了", "name": "名前", "notes": "説明", "due_on": "期日", "assignee": "担当者"}[k]
}

// DescribeOp is a one-line description of an outbox entry: "task: what it changes"
// (see DescribeChange). Entries whose change already names what they are about (creating
// or duplicating a task, renaming a section) are not prefixed.
func DescribeOp(op Op) string {
	what := DescribeChange(op)
	switch op.Kind {
	case OpCreateTask, OpDuplicateTask, OpRenameSection:
		return what
	}
	name := op.TaskName
	if name == "" {
		name = op.TaskGID
	}
	return name + ": " + what
}

// DescribeChange says in a few words what an outbox entry changes, without naming the
// task (e.g. 「名前を「…」に変更」「コメント「…」」). It is the one vocabulary for outbox
// entries: DescribeOp adds the task, and dropped changes are recorded with it (Discard.Summary).
func DescribeChange(op Op) string {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	switch op.Kind {
	case OpCreateTask:
		if str("parent") != "" {
			return fmt.Sprintf("サブタスク「%s」の作成", str("name"))
		}
		return fmt.Sprintf("タスク「%s」の作成", str("name"))
	case OpDuplicateTask:
		return fmt.Sprintf("複製「%s」の作成", str("name"))
	case OpAddComment:
		return fmt.Sprintf("コメント「%s」", clip(str("text"), 40))
	case OpMoveTask:
		return fmt.Sprintf("セクション「%s」への移動", str("section_name"))
	case OpAddTag:
		return fmt.Sprintf("タグ「%s」の追加", str("tag_name"))
	case OpRemoveTag:
		if str("tag_name") != "" {
			return fmt.Sprintf("タグ「%s」の削除", str("tag_name"))
		}
		return "タグの削除"
	case OpAddFollower:
		return fmt.Sprintf("フォロワー %s の追加", str("user_name"))
	case OpRemoveFollower:
		return fmt.Sprintf("フォロワー %s の削除", str("user_name"))
	case OpAddProject:
		return fmt.Sprintf("プロジェクト「%s」への追加", str("project_name"))
	case OpRemoveProject:
		return fmt.Sprintf("プロジェクト「%s」からの削除", str("project_name"))
	case OpUploadAttachment:
		return fmt.Sprintf("添付「%s」", str("name"))
	case OpCreateStatus:
		if str("title") == "" {
			return fmt.Sprintf("ステータス更新（%s）", StatusLabel(str("status_type")))
		}
		return fmt.Sprintf("ステータス更新「%s」", str("title"))
	case OpRenameSection:
		if old, _ := op.Base["name"].(string); old != "" {
			return fmt.Sprintf("セクション名を「%s」から「%s」に変更", old, str("name"))
		}
		return fmt.Sprintf("セクション名を「%s」に変更", str("name"))
	case OpUpdateTask:
		var parts []string
		for _, k := range []string{"name", "completed", "due_on", "assignee", "notes", "liked"} {
			v, ok := op.Payload[k]
			if !ok {
				continue
			}
			switch k {
			case "name":
				parts = append(parts, fmt.Sprintf("名前を「%s」に変更", v))
			case "completed":
				parts = append(parts, map[bool]string{true: "完了にする", false: "未完了に戻す"}[v == true])
			case "due_on":
				if v == "" {
					parts = append(parts, "期日を削除")
				} else {
					parts = append(parts, fmt.Sprintf("期日を %s に変更", v))
				}
			case "assignee":
				parts = append(parts, "担当者の変更")
			case "notes":
				parts = append(parts, "説明の変更")
			case "liked":
				parts = append(parts, "いいね")
			}
		}
		if len(parts) == 0 && op.Payload["html_notes"] != nil {
			parts = append(parts, "説明の変更")
		}
		return strings.Join(parts, "、")
	}
	return op.Kind
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
