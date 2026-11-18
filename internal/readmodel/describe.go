package readmodel

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/task"
)

// Discard is a local change dropped because its target was deleted in Asana.
type Discard struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	TaskGID     string `json:"task_gid,omitempty"`
	TaskName    string `json:"task_name,omitempty"`
	Summary     string `json:"summary"` // what the change was, e.g. 「名前を「…」に変更」
	Reason      string `json:"reason,omitempty"`
	DiscardedAt string `json:"discarded_at"`
	// Payload is the dropped change as it was queued (e.g. the full comment text, the new
	// notes, the path of the attachment's local copy, which is kept), to recover it.
	Payload map[string]any `json:"payload,omitempty"`
}

// ResetReason is the reason recorded for unsent changes deleted by Reset.
const ResetReason = "ローカルのデータを初期化したため、送らずに破棄しました"

// StatusTypes are the status values of a project status update, with Japanese labels.
var StatusTypes = []struct{ Value, Label string }{
	{"on_track", "順調"}, {"at_risk", "要注意"}, {"off_track", "遅れ"}, {"on_hold", "保留"}, {"complete", "完了"},
}

// StatusLabel returns the Japanese label of a status type.
func StatusLabel(t string) string {
	for _, s := range StatusTypes {
		if s.Value == t {
			return s.Label
		}
	}
	return t
}

// Human-readable (Japanese) descriptions of outbox entries, shared by the TUI and the CLI.

// VerifyNote tells the user that an entry being checked against Asana needs nothing from them.
const VerifyNote = "操作は不要です。次の同期で自動的に確認します"

// AmbiguousMessage is the last_error of an entry that may have reached Asana, when Asana
// has several candidates for what it created.
const AmbiguousMessage = "Asana に届いたか自動で確かめられませんでした（同じ内容のものが複数あります）。Asana で確認して、届いていなければ再送、届いていれば破棄してください"

// OpLabel is OpStateLabel, except that a queued entry Asana may already have applied
// (Verify: Push checks Asana by itself before sending it again) is "自動確認中".
func OpLabel(op outbox.Op) string {
	if op.State == outbox.StatePending && op.Verify {
		return "自動確認中"
	}
	return OpStateLabel(op.State)
}

// OpNote is the note to show under an entry ("" for none): heading is "エラー" for a
// failed entry and "状況" otherwise. An entry being checked always gets a note saying
// that the user does not need to do anything.
func OpNote(op outbox.Op) (heading, text string) {
	if op.State == outbox.StateFailed {
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
// (the editors and the MCP tools take Markdown): toMarkdown converts it, and is
// richtext.ToEditableMarkdown in the callers (the store does not convert rich text).
func DiscardDetails(d Discard, toMarkdown func(html string) string) []DiscardDetail {
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
			if md := strings.TrimSpace(toMarkdown(h)); md != "" {
				out = append(out, DiscardDetail{label + "（Markdown）", md})
				return
			}
		}
		add(label, key)
	}
	switch d.Kind {
	case outbox.KindAddComment:
		addRich("コメント", "text", "html_text")
	case outbox.KindCreateStatus:
		add("タイトル", "title")
		addRich("本文", "text", "html_text")
	case outbox.KindUploadAttachment:
		add("ファイルのコピー", "path")
	case outbox.KindCreateTask, outbox.KindUpdateTask:
		add("名前", "name")
		addRich("説明", "notes", "html_notes")
	}
	return out
}

// AccountChangedHelp explains a sync stopped because the logged-in Asana account is not
// the one the local data belongs to (sync.ErrAccountChanged): what happened, how many
// changes of the previous account are unsent, and the two ways out. resetHow says how to
// reset in the caller's UI (e.g. "asagao reset").
func AccountChangedHelp(unsent int, resetHow string) string {
	return fmt.Sprintf("ログイン中の Asana アカウントが、ローカルのデータを同期したアカウントと違うため、同期を止めています"+
		"（前のアカウントの未送信の変更: %d 件）。前のアカウントでログインし直せば、そのまま送られます（asagao auth login）。"+
		"新しいアカウントで使うなら、ローカルのデータを初期化してください（%s。未送信の変更は消す前に自動でバックアップします）", unsent, resetHow)
}

// FieldLabel is the label of an update_task field ("" for unknown keys).
func FieldLabel(k string) string {
	return map[string]string{"completed": "完了", "name": "名前", "notes": "説明", "due_on": "期日", "assignee": "担当者",
		"liked": "いいね", "resource_subtype": "種類", "approval_status": "承認"}[k]
}

// ConflictLines describes the conflicting fields of an entry in conflict, one per line:
// "label: こちら=local / Asana=server".
func ConflictLines(op outbox.Op) []string {
	if op.Kind == outbox.KindSetCustomField {
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
func DescribeOp(op outbox.Op) string {
	what := DescribeChange(op)
	switch op.Kind {
	case outbox.KindCreateTask, outbox.KindDuplicateTask, outbox.KindRenameSection:
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
func DescribeChange(op outbox.Op) string {
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	switch op.Kind {
	case outbox.KindCreateTask:
		if str("parent") != "" {
			return fmt.Sprintf("サブタスク「%s」の作成", str("name"))
		}
		return fmt.Sprintf("タスク「%s」の作成", str("name"))
	case outbox.KindDuplicateTask:
		return fmt.Sprintf("複製「%s」の作成", str("name"))
	case outbox.KindAddComment:
		return fmt.Sprintf("コメント「%s」", clip(str("text"), 40))
	case outbox.KindMoveTask:
		if str("insert_before") != "" || str("insert_after") != "" {
			return fmt.Sprintf("セクション「%s」への移動 (並び順の指定あり)", str("section_name"))
		}
		return fmt.Sprintf("セクション「%s」への移動", str("section_name"))
	case outbox.KindDeleteTask:
		return "タスクの削除"
	case outbox.KindSetParent:
		if str("parent") == "" {
			return "親タスクから外す"
		}
		return fmt.Sprintf("「%s」のサブタスクにする", str("parent_name"))
	case outbox.KindAddTag:
		return fmt.Sprintf("タグ「%s」の追加", str("tag_name"))
	case outbox.KindRemoveTag:
		if str("tag_name") != "" {
			return fmt.Sprintf("タグ「%s」の削除", str("tag_name"))
		}
		return "タグの削除"
	case outbox.KindAddFollower:
		return fmt.Sprintf("フォロワー %s の追加", str("user_name"))
	case outbox.KindRemoveFollower:
		return fmt.Sprintf("フォロワー %s の削除", str("user_name"))
	case outbox.KindAddProject:
		return fmt.Sprintf("プロジェクト「%s」への追加", str("project_name"))
	case outbox.KindRemoveProject:
		return fmt.Sprintf("プロジェクト「%s」からの削除", str("project_name"))
	case outbox.KindUploadAttachment:
		return fmt.Sprintf("添付「%s」", str("name"))
	case outbox.KindAddDependency:
		return fmt.Sprintf("「%s」を待つ", dependencyName(op))
	case outbox.KindRemoveDependency:
		return fmt.Sprintf("「%s」を待つのをやめる", dependencyName(op))
	case outbox.KindSetCustomField:
		if op.Payload["value"] == nil {
			return fmt.Sprintf("「%s」を空にする", str("field_name"))
		}
		return fmt.Sprintf("「%s」を「%s」に変更", str("field_name"), clip(str("display"), 40))
	case outbox.KindCreateStatus:
		if str("title") == "" {
			return fmt.Sprintf("ステータス更新（%s）", StatusLabel(str("status_type")))
		}
		return fmt.Sprintf("ステータス更新「%s」", str("title"))
	case outbox.KindMarkNotification:
		if op.Payload["read"] == true {
			return "通知を既読にする"
		}
		return "通知を未読に戻す"
	case outbox.KindRenameSection:
		if old, _ := op.Base["name"].(string); old != "" {
			return fmt.Sprintf("セクション名を「%s」から「%s」に変更", old, str("name"))
		}
		return fmt.Sprintf("セクション名を「%s」に変更", str("name"))
	case outbox.KindUpdateTask:
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
				parts = append(parts, fmt.Sprintf("種類を「%s」に変更", task.SubtypeLabel(fmt.Sprint(v))))
			case "approval_status":
				parts = append(parts, fmt.Sprintf("承認を「%s」に変更", task.ApprovalLabel(fmt.Sprint(v))))
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

func dependencyName(op outbox.Op) string {
	if name, _ := op.Payload["dependency_name"].(string); name != "" {
		return name
	}
	dep, _ := op.Payload["dependency"].(string)
	return dep
}

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
		parts = append(parts, "["+task.ApprovalLabel(status)+"]")
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
