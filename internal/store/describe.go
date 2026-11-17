package store

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/amasok/asana-tui/internal/domain/task"
	"github.com/amasok/asana-tui/internal/richtext"
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

// DiscardLine describes one dropped change: 「task」: what it changed (and, for an unsent
// change deleted by a reset, that it was).
func DiscardLine(d Discard) string {
	s := d.Summary
	if d.TaskName != "" {
		s = "「" + d.TaskName + "」: " + s
	}
	if IsResetDiscard(d) {
		s += "（初期化で破棄）"
	}
	return s
}

// IsResetDiscard reports whether d was an unsent change deleted by Reset (which the user
// confirmed), rather than a change to something deleted in Asana.
func IsResetDiscard(d Discard) bool { return d.Reason == ResetReason }

// DiscardDetail is a piece of a dropped change worth keeping in full, e.g. a comment.
type DiscardDetail struct {
	Label string
	Text  string
}

// DiscardDetails returns, in full, what the user wrote or attached in a dropped change
// (name, description, comment or status text, the local copy of an attachment), so it can
// be recovered. Rich text is given as Markdown, which keeps the formatting when pasted back
// (the editors and the MCP tools take Markdown).
func DiscardDetails(d Discard) []DiscardDetail {
	str := func(k string) string { v, _ := d.Payload[k].(string); return v }
	var out []DiscardDetail
	add := func(label, key string) {
		if v := str(key); v != "" {
			out = append(out, DiscardDetail{label, v})
		}
	}
	// addRich prefers the rich text (htmlKey) as Markdown over the plain text (key).
	addRich := func(label, key, htmlKey string) {
		if h := str(htmlKey); h != "" {
			if md := strings.TrimSpace(richtext.ToEditableMarkdown(h)); md != "" {
				out = append(out, DiscardDetail{label + "（Markdown）", md})
				return
			}
		}
		add(label, key)
	}
	switch d.Kind {
	case OpAddComment:
		addRich("コメント", "text", "html_text")
	case OpCreateStatus:
		add("タイトル", "title")
		addRich("本文", "text", "html_text")
	case OpUploadAttachment:
		add("ファイルのコピー", "path")
	case OpCreateTask, OpUpdateTask:
		add("名前", "name")
		addRich("説明", "notes", "html_notes")
	}
	return out
}

// AccountChangedHelp explains a sync stopped because the logged-in Asana account is not
// the one the local data belongs to (sync.ErrAccountChanged): what happened, how many
// changes of the previous account are unsent, and the two ways out. resetHow says how to
// reset in the caller's UI (e.g. "asana-tui reset").
func AccountChangedHelp(unsent int, resetHow string) string {
	return fmt.Sprintf("ログイン中の Asana アカウントが、ローカルのデータを同期したアカウントと違うため、同期を止めています"+
		"（前のアカウントの未送信の変更: %d 件）。前のアカウントでログインし直せば、そのまま送られます（asana-tui auth login）。"+
		"新しいアカウントで使うなら、ローカルのデータを初期化してください（%s。未送信の変更は消す前に自動でバックアップします）", unsent, resetHow)
}

// FieldLabel is the label of an update_task field ("" for unknown keys).
func FieldLabel(k string) string {
	return map[string]string{"completed": "完了", "name": "名前", "notes": "説明", "due_on": "期日", "assignee": "担当者",
		"liked": "いいね", "resource_subtype": "種類", "approval_status": "承認"}[k]
}

// ConflictLines describes the conflicting fields of an entry in conflict, one per line:
// "label: こちら=local / Asana=server".
func ConflictLines(op Op) []string {
	if op.Kind == OpSetCustomField {
		text := func(m map[string]any) string {
			if s, _ := m["display"].(string); s != "" {
				return s
			}
			if m["value"] == nil {
				return "（空）"
			}
			return fmt.Sprint(m["value"])
		}
		name, _ := op.Payload["field_name"].(string)
		return []string{fmt.Sprintf("%s: こちら=%s / Asana=%s", name, text(op.Payload), text(op.Conflict))}
	}
	keys := slices.Sorted(maps.Keys(op.Conflict))
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, fmt.Sprintf("%s: こちら=%v / Asana=%v", FieldLabel(k), op.Payload[k], op.Conflict[k]))
	}
	return lines
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
	case OpAddDependency:
		return fmt.Sprintf("「%s」を待つ", dependencyName(op))
	case OpRemoveDependency:
		return fmt.Sprintf("「%s」を待つのをやめる", dependencyName(op))
	case OpSetCustomField:
		if op.Payload["value"] == nil {
			return fmt.Sprintf("「%s」を空にする", str("field_name"))
		}
		return fmt.Sprintf("「%s」を「%s」に変更", str("field_name"), clip(str("display"), 40))
	case OpCreateStatus:
		if str("title") == "" {
			return fmt.Sprintf("ステータス更新（%s）", StatusLabel(str("status_type")))
		}
		return fmt.Sprintf("ステータス更新「%s」", str("title"))
	case OpMarkNotification:
		if op.Payload["read"] == true {
			return "通知を既読にする"
		}
		return "通知を未読に戻す"
	case OpRenameSection:
		if old, _ := op.Base["name"].(string); old != "" {
			return fmt.Sprintf("セクション名を「%s」から「%s」に変更", old, str("name"))
		}
		return fmt.Sprintf("セクション名を「%s」に変更", str("name"))
	case OpUpdateTask:
		var parts []string
		for _, k := range []string{"name", "completed", "due_on", "assignee", "notes", "liked", "resource_subtype", "approval_status"} {
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
				if v == "" {
					parts = append(parts, "担当者を外す")
				} else {
					parts = append(parts, "担当者の変更")
				}
			case "notes":
				parts = append(parts, "説明の変更")
			case "resource_subtype":
				parts = append(parts, fmt.Sprintf("種類を「%s」に変更", SubtypeLabel(fmt.Sprint(v))))
			case "approval_status":
				parts = append(parts, fmt.Sprintf("承認を「%s」に変更", ApprovalLabel(fmt.Sprint(v))))
			case "liked":
				if v == true {
					parts = append(parts, "いいね")
				} else {
					parts = append(parts, "いいねを外す")
				}
			}
		}
		if len(parts) == 0 && op.Payload["html_notes"] != nil {
			parts = append(parts, "説明の変更")
		}
		return strings.Join(parts, "、")
	}
	return op.Kind
}

func dependencyName(op Op) string {
	if name, _ := op.Payload["dependency_name"].(string); name != "" {
		return name
	}
	dep, _ := op.Payload["dependency"].(string)
	return dep
}

// SubtypeLabel is task.SubtypeLabel (kept here until the entry points stop using store).
func SubtypeLabel(s string) string { return task.SubtypeLabel(s) }

// ApprovalLabel is task.ApprovalLabel.
func ApprovalLabel(s string) string { return task.ApprovalLabel(s) }

// ParseSubtype is task.ParseSubtype.
func ParseSubtype(s string) (string, error) { return task.ParseSubtype(s) }

// ParseApprovalStatus is task.ParseApprovalStatus.
func ParseApprovalStatus(s string) (string, error) { return task.ParseApprovalStatus(s) }

// TaskBadges are short marks for a task in a list: ◆ for a milestone, the approval status
// of an approval task, and how many incomplete tasks it waits for.
func TaskBadges(t TaskSummary) string {
	var parts []string
	switch t.ResourceSubtype {
	case "milestone":
		parts = append(parts, "◆")
	case "approval":
		status := t.ApprovalStatus
		if status == "" {
			status = "pending"
		}
		parts = append(parts, "["+ApprovalLabel(status)+"]")
	}
	if t.OpenDependencies > 0 && !t.Completed {
		parts = append(parts, fmt.Sprintf("[待ち%d]", t.OpenDependencies))
	}
	return strings.Join(parts, " ")
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
