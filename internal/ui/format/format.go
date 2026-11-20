// Package format holds human-readable (Japanese) UI labels and descriptions of outbox
// entries, dropped changes, status updates, and task badges, shared by the TUI, CLI,
// and MCP server.
package format

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/project"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/domain/timetrack"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

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
	return termsafe.String(t)
}

// VerifyNote tells the user that an entry being checked against Asana needs nothing from them.
const VerifyNote = "操作は不要です。次の同期で自動的に確認します"

// AmbiguousMessage is the last_error of an entry that may have reached Asana, when Asana
// has several candidates for what it created.
const AmbiguousMessage = "Asana に届いたか自動で確かめられませんでした（同じ内容のものが複数あります）。Asana で確認して、届いていなければ再送、届いていれば破棄してください"

// BriefForbiddenMessage says that Asana does not let us read or change one project's
// brief (it answered 403; BriefUnavailable).
const BriefForbiddenMessage = "このプロジェクトのブリーフは、Asana が読み書きを許していません (403。権限がないか、API がプレビューのため)。概要を開き直すと、もう一度確かめます"

// OpStateLabel is the Japanese name of an outbox entry's state.
func OpStateLabel(state string) string {
	switch state {
	case outbox.StatePending:
		return "未送信"
	case outbox.StateConflict:
		return "競合"
	case outbox.StateFailed:
		return "失敗"
	case outbox.StateSending:
		return "送信中"
	}
	return termsafe.String(state)
}

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
		return "エラー", termsafe.String(op.LastError)
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
	return "状況", termsafe.String(text)
}

// DiscardNotice heads the one-time notice about changes dropped because what they changed
// (a task, or its section, project, tag...) was deleted in Asana (see PendingDiscards).
const DiscardNotice = "Asana で削除されたものへの変更を破棄しました"

// DiscardLine describes one dropped change: 「task」: what it changed (and, for an unsent
// change deleted by a reset, that it was).
func DiscardLine(d readmodel.Discard) string {
	s := d.Summary
	if d.TaskName != "" {
		s = "「" + d.TaskName + "」: " + s
	}
	if IsResetDiscard(d) {
		s += "（初期化で破棄）"
	}
	return termsafe.String(s)
}

// IsResetDiscard reports whether d was an unsent change deleted by Reset (which the user
// confirmed), rather than a change to something deleted in Asana.
func IsResetDiscard(d readmodel.Discard) bool { return d.Reason == ResetReason }

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
func DiscardDetails(d readmodel.Discard, toMarkdown func(html string) string) []DiscardDetail {
	str := func(k string) string { v, _ := d.Payload[k].(string); return v }
	var out []DiscardDetail
	add := func(label, key string) {
		if v := str(key); v != "" {
			out = append(out, DiscardDetail{label, termsafe.String(v)})
		}
	}
	// addRich prefers the rich text (htmlKey) as Markdown over the plain text (key).
	addRich := func(label, key, htmlKey string) {
		if h := str(htmlKey); h != "" {
			if md := strings.TrimSpace(toMarkdown(h)); md != "" {
				out = append(out, DiscardDetail{label + "（Markdown）", termsafe.String(md)})
				return
			}
		}
		add(label, key)
	}
	switch d.Kind {
	case outbox.KindAddComment, outbox.KindUpdateStory:
		addRich("コメント", "text", "html_text")
	case outbox.KindCreateStatus:
		add("タイトル", "title")
		addRich("本文", "text", "html_text")
	case outbox.KindUploadAttachment:
		add("ファイルのコピー", "path")
	case outbox.KindCreateTask, outbox.KindUpdateTask:
		add("名前", "name")
		addRich("説明", "notes", "html_notes")
	case outbox.KindUpdateProject:
		addRich("説明", "notes", "html_notes")
	case outbox.KindUpdateProjectBrief:
		add("ブリーフのタイトル", "title")
		addRich("ブリーフ", "text", "html_text")
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
	return map[string]string{"completed": "完了", "name": "名前", "notes": "説明", "due_on": "期日", "start_on": "開始日", "assignee": "担当者",
		"liked": "いいね", "resource_subtype": "種類", "approval_status": "承認",
		"custom_type": "カスタムタイプ", "custom_type_status_option": "ステータス"}[k]
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
		return []string{termsafe.String(fmt.Sprintf("%s: こちら=%s / Asana=%s", name, text(op.Payload), text(op.Conflict)))}
	}
	if op.Kind == outbox.KindUpdateProjectBrief {
		return BriefConflictLines(op)
	}
	label := FieldLabel
	if op.Kind == outbox.KindUpdateProject {
		label = project.FieldLabel
	}
	keys := slices.Sorted(maps.Keys(op.Conflict))
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, termsafe.String(fmt.Sprintf("%s: こちら=%v / Asana=%v", label(k), op.Payload[k], op.Conflict[k])))
	}
	return lines
}

// DescribeOp is a one-line description of an outbox entry: "task: what it changes"
// (see DescribeChange). Entries whose change already names what they are about (creating
// or duplicating a task, renaming a section) are not prefixed.
func DescribeOp(op outbox.Op) string {
	what := DescribeChange(op)
	switch op.Kind {
	case outbox.KindCreateTask, outbox.KindDuplicateTask, outbox.KindRenameSection,
		outbox.KindCreateSection, outbox.KindDeleteSection, outbox.KindInsertSection,
		outbox.KindUpdateTag, outbox.KindDeleteTag, outbox.KindCreateEnumOption:
		return what
	}
	name := op.TaskName
	if name == "" {
		name = op.TaskGID
	}
	return termsafe.String(name) + ": " + what
}

// DescribeChange says in a few words what an outbox entry changes, without naming the
// task (e.g. 「名前を「…」に変更」「コメント「…」」). It is the one vocabulary for outbox
// entries: DescribeOp adds the task, and dropped changes are recorded with it (Discard.Summary).
func DescribeChange(op outbox.Op) string {
	return termsafe.String(describeChange(op))
}

func describeChange(op outbox.Op) string {
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
	case outbox.KindUpdateStory:
		return fmt.Sprintf("コメントを「%s」に編集", clip(str("text"), 40))
	case outbox.KindDeleteStory:
		return fmt.Sprintf("コメント「%s」の削除", clip(str("text"), 40))
	case outbox.KindDeleteAttachment:
		return fmt.Sprintf("添付「%s」の削除", str("name"))
	case outbox.KindAddTimeEntry:
		return fmt.Sprintf("作業時間 %s (%s) の記録", timetrack.Format(intOf(op.Payload["duration_minutes"])), str("entered_on"))
	case outbox.KindUpdateTimeEntry:
		return fmt.Sprintf("作業時間を %s (%s) に変更", timetrack.Format(intOf(op.Payload["duration_minutes"])), str("entered_on"))
	case outbox.KindDeleteTimeEntry:
		return fmt.Sprintf("作業時間 %s (%s) の削除", timetrack.Format(intOf(op.Payload["duration_minutes"])), str("entered_on"))
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
	case outbox.KindCreateSection:
		return fmt.Sprintf("セクション「%s」の作成", str("name"))
	case outbox.KindDeleteSection:
		return fmt.Sprintf("セクション「%s」の削除", str("name"))
	case outbox.KindInsertSection:
		return fmt.Sprintf("セクション「%s」の並べ替え", str("name"))
	case outbox.KindUpdateTag:
		old, _ := op.Base["name"].(string)
		if old == "" {
			old, _ = op.Payload["name"].(string)
		}
		var parts []string
		if v, ok := op.Payload["name"].(string); ok {
			parts = append(parts, fmt.Sprintf("名前を「%s」に変更", v))
		}
		if v, ok := op.Payload["color"].(string); ok {
			if v == "" {
				v = "なし"
			}
			parts = append(parts, fmt.Sprintf("色を %s に変更", v))
		}
		return fmt.Sprintf("タグ「%s」の%s", old, strings.Join(parts, "、"))
	case outbox.KindDeleteTag:
		return fmt.Sprintf("タグ「%s」の削除", str("name"))
	case outbox.KindMoveMySection:
		return fmt.Sprintf("マイタスクのセクション「%s」への移動", str("section_name"))
	case outbox.KindCreateEnumOption:
		return fmt.Sprintf("カスタムフィールド「%s」に選択肢「%s」を追加", str("field_name"), str("name"))
	case outbox.KindAddFieldSetting:
		return fmt.Sprintf("カスタムフィールド「%s」をプロジェクトに追加", str("field_name"))
	case outbox.KindRemoveFieldSetting:
		return fmt.Sprintf("カスタムフィールド「%s」をプロジェクトから外す", str("field_name"))
	case outbox.KindUpdateProjectBrief:
		if op.Payload["delete"] == true {
			return "ブリーフの削除"
		}
		if state, _ := op.Base["state"].(string); state != readmodel.BriefAvailable {
			return fmt.Sprintf("ブリーフ「%s」の作成", clip(str("title"), 40))
		}
		if _, ok := op.Payload["title"]; !ok { // the body only
			return "ブリーフの本文の変更"
		}
		return fmt.Sprintf("ブリーフ「%s」の変更", clip(str("title"), 40))
	case outbox.KindUpdateProject:
		var parts []string
		for _, k := range []string{"notes", "html_notes", "owner", "start_on", "due_on", "color"} {
			v, ok := op.Payload[k]
			if !ok {
				continue
			}
			label := project.FieldLabel(k)
			switch {
			case k == "html_notes":
				if _, plain := op.Payload["notes"]; !plain {
					parts = append(parts, "説明の変更")
				}
			case k == "notes", k == "owner" && v != "":
				parts = append(parts, label+"の変更")
			case v == "":
				parts = append(parts, label+"を外す")
			default:
				parts = append(parts, fmt.Sprintf("%sを %v に変更", label, v))
			}
		}
		return "プロジェクトの" + strings.Join(parts, "、")
	case outbox.KindRenameSection:
		if old, _ := op.Base["name"].(string); old != "" {
			return fmt.Sprintf("セクション名を「%s」から「%s」に変更", old, str("name"))
		}
		return fmt.Sprintf("セクション名を「%s」に変更", str("name"))
	case outbox.KindUpdateTask:
		var parts []string
		for _, k := range []string{"name", "completed", "start_on", "due_on", "assignee", "notes", "liked", "resource_subtype", "approval_status", "custom_type", "custom_type_status_option"} {
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
			case "start_on":
				if v == "" {
					parts = append(parts, "開始日を削除")
				} else {
					parts = append(parts, fmt.Sprintf("開始日を %s に変更", v))
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
				if _, typ := op.Payload["custom_type"]; !typ || v != task.SubtypeCustom { // else said by custom_type
					parts = append(parts, fmt.Sprintf("種類を「%s」に変更", task.SubtypeLabel(fmt.Sprint(v))))
				}
			case "custom_type":
				parts = append(parts, "種類をカスタムタイプに変更")
			case "custom_type_status_option":
				parts = append(parts, "ステータスの変更")
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

// KindLabel is a task's type as it is shown: the name of its custom type, if it has one,
// else the Japanese name of its resource_subtype.
func KindLabel(t readmodel.TaskSummary) string {
	if t.ResourceSubtype == "custom" && t.CustomType != nil && t.CustomType.Name != "" {
		return termsafe.String(t.CustomType.Name)
	}
	return termsafe.String(task.SubtypeLabel(t.ResourceSubtype))
}

// ReactionsLine shows a comment's reactions in one line, e.g. "👍 2  🎉 1" ("" for none).
// The emoji the user reacted with is marked with "*".
func ReactionsLine(rs []readmodel.Reaction) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.Count <= 0 {
			continue
		}
		emoji := r.Variant
		if emoji == "" {
			emoji = r.EmojiBase
		}
		mark := ""
		if r.Reacted {
			mark = "*"
		}
		parts = append(parts, fmt.Sprintf("%s%s %d", emoji, mark, r.Count))
	}
	return termsafe.String(strings.Join(parts, "  "))
}

// TaskBadges are short marks for a task in a list: ◆ for a milestone, the approval status
// of an approval task, the custom type and status of a custom one, and how many incomplete
// tasks it waits for.
func TaskBadges(t readmodel.TaskSummary) string {
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
	case task.SubtypeCustom:
		// A custom task type (#59) and its status: "[バグ: 対応中]".
		if label := KindLabel(t); t.CustomTypeStatus != nil && t.CustomTypeStatus.Name != "" {
			parts = append(parts, "["+label+": "+t.CustomTypeStatus.Name+"]")
		} else {
			parts = append(parts, "["+label+"]")
		}
	}
	if t.OpenDependencies > 0 && !t.Completed {
		parts = append(parts, fmt.Sprintf("[待ち%d]", t.OpenDependencies))
	}
	return termsafe.String(strings.Join(parts, " "))
}

// ProjectGroup is the heading a project is listed under (ListProjects' order): ★ お気に入り,
// its team, or チームなし.
func ProjectGroup(p readmodel.ProjectView) string {
	switch {
	case p.Favorite:
		return "★ お気に入り"
	case p.TeamName != "":
		return termsafe.String(p.TeamName)
	case p.TeamGID != "":
		return termsafe.String(p.TeamGID)
	}
	return "チームなし"
}

// GoalStatusLabel names a goal's status in Japanese.
func GoalStatusLabel(status string) string {
	switch status {
	case "green":
		return "順調"
	case "yellow":
		return "要注意"
	case "red":
		return "遅れ"
	case "achieved":
		return "達成"
	case "partial":
		return "一部達成"
	case "missed":
		return "未達"
	case "dropped":
		return "中止"
	case "":
		return "未設定"
	}
	return termsafe.String(status)
}

// goalMetricPercent is the progress in whole percent, rounded down: computed as (current - initial)
// * 100 / span so that e.g. 29 of 100 is 29 (29/100*100 is 28.999…), with a margin for
// what is left of the rounding.
func goalMetricPercent(m *readmodel.GoalMetric) (int, bool) {
	if _, ok := m.Progress(); !ok {
		return 0, false
	}
	start := 0.0
	if m.Initial != nil {
		start = *m.Initial
	}
	pct := math.Floor((*m.Current-start)*100/(*m.Target-start) + 1e-9)
	if math.IsNaN(pct) {
		return 0, false
	}
	return int(math.Max(0, math.Min(100, pct))), true
}

// GoalMetricText describes a goal metric's progress, e.g. "40 / 100（40%）".
func GoalMetricText(m *readmodel.GoalMetric) string {
	if m == nil {
		return ""
	}
	num := func(v *float64) string {
		if v == nil {
			return ""
		}
		x := *v
		if m.Kind == "percentage" {
			x *= 100 // Asana keeps a percentage as a fraction (1 is 100%)
		}
		s := strconv.FormatFloat(x, 'f', max(0, min(m.Precision, 6)), 64)
		switch m.Kind {
		case "percentage":
			return s + "%"
		case "currency":
			return strings.TrimSpace(m.Currency + " " + s)
		}
		return s
	}
	cur := m.Display
	if cur == "" || (m.Kind == "percentage" && m.Current != nil) {
		// Asana's display of a percentage is "0%" in a GET but "0.00" in other answers
		// (#31): build it from the value instead.
		cur = num(m.Current)
	}
	text := cur
	if t := num(m.Target); t != "" {
		text = strings.TrimSpace(cur + " / " + t)
	}
	if pct, ok := goalMetricPercent(m); ok {
		text += "（" + strconv.Itoa(pct) + "%）"
	}
	return termsafe.String(text)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// intOf reads a whole number from an outbox payload (JSON numbers decode as float64).
func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// BriefConflictLines describes a brief change in conflict (#60): how the brief is here and
// in Asana, one line for the title and one for the text (or that it was deleted or made
// there). The conflict holds Asana's brief as the base does: state, title, text.
func BriefConflictLines(op outbox.Op) []string {
	str := func(m map[string]any, k string) string { v, _ := m[k].(string); return v }
	local := func(k string) string {
		if op.Payload["delete"] == true {
			return "（削除）"
		}
		return clip(str(op.Payload, k), 60)
	}
	if str(op.Conflict, "state") != readmodel.BriefAvailable {
		return []string{termsafe.String("ブリーフ: こちら=" + local("title") + " / Asana=（ブリーフがありません。削除されています）")}
	}
	var lines []string
	del := op.Payload["delete"] == true
	_, title := op.Payload["title"] // a change sends only what it changes
	_, text := op.Payload["text"]
	if del || title && str(op.Payload, "title") != str(op.Conflict, "title") {
		lines = append(lines, termsafe.String(fmt.Sprintf("ブリーフのタイトル: こちら=%s / Asana=%s", local("title"), clip(str(op.Conflict, "title"), 60))))
	}
	if del || text && str(op.Payload, "text") != str(op.Conflict, "text") {
		lines = append(lines, termsafe.String(fmt.Sprintf("ブリーフ: こちら=%s / Asana=%s", local("text"), clip(str(op.Conflict, "text"), 60))))
	}
	if len(lines) == 0 {
		lines = append(lines, "ブリーフ: Asana でも変更されています")
	}
	return lines
}
