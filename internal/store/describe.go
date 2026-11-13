package store

import (
	"fmt"
	"strings"
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
	}
	return state
}

// FieldLabel is the label of an update_task field ("" for unknown keys).
func FieldLabel(k string) string {
	return map[string]string{"completed": "完了", "name": "名前", "notes": "説明", "due_on": "期日", "assignee": "担当者"}[k]
}

// DescribeOp is a one-line, human-readable description of an outbox entry.
func DescribeOp(op Op) string {
	name := op.TaskName
	if name == "" {
		name = op.TaskGID
	}
	switch op.Kind {
	case OpCreateTask:
		return "作成: " + name
	case OpMoveTask:
		return fmt.Sprintf("移動: %s → %v", name, op.Payload["section_name"])
	case OpRenameSection:
		return fmt.Sprintf("セクション名の変更: %v → %v", op.Base["name"], op.Payload["name"])
	case OpCreateStatus:
		return fmt.Sprintf("ステータス更新: %s（%s）", name, StatusLabel(fmt.Sprint(op.Payload["status_type"])))
	case OpAddFollower:
		return fmt.Sprintf("フォロワーを追加: %s %v", name, op.Payload["user_name"])
	case OpRemoveFollower:
		return fmt.Sprintf("フォロワーを外す: %s %v", name, op.Payload["user_name"])
	case OpAddProject:
		return fmt.Sprintf("プロジェクトに追加: %s → %v", name, op.Payload["project_name"])
	case OpRemoveProject:
		return fmt.Sprintf("プロジェクトから外す: %s ← %v", name, op.Payload["project_name"])
	case OpDuplicateTask:
		return "複製: " + name
	case OpUploadAttachment:
		return fmt.Sprintf("ファイルを添付: %s ← %v", name, op.Payload["name"])
	case OpAddTag:
		return fmt.Sprintf("タグを付ける: %s #%v", name, op.Payload["tag_name"])
	case OpRemoveTag:
		return fmt.Sprintf("タグを外す: %s #%v", name, op.Payload["tag_name"])
	case OpAddComment:
		text, _ := op.Payload["text"].(string)
		if r := []rune(text); len(r) > 30 {
			text = string(r[:30]) + "…"
		}
		return fmt.Sprintf("コメント: %s「%s」", name, text)
	}
	var parts []string
	for _, k := range []string{"completed", "name", "notes", "due_on", "assignee"} {
		v, ok := op.Payload[k]
		if !ok {
			continue
		}
		switch k {
		case "completed":
			if v == true {
				parts = append(parts, "完了にする")
			} else {
				parts = append(parts, "未完了に戻す")
			}
		case "name":
			parts = append(parts, fmt.Sprintf("名前→%v", v))
		case "notes":
			parts = append(parts, "説明を変更")
		case "due_on":
			if v == "" {
				parts = append(parts, "期日を削除")
			} else {
				parts = append(parts, fmt.Sprintf("期日→%v", v))
			}
		case "assignee":
			parts = append(parts, "担当者を変更")
		}
	}
	return name + ": " + strings.Join(parts, "、")
}
