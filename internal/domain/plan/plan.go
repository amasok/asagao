package plan

import (
	"time"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// A feature the workspace's plan lacks (Asana answered 402) is recorded per workspace, so
// a 402 in a free workspace does not hide the feature in a paid organization, and only
// for RecheckAfter: it is then offered and tried again, so a workspace whose plan was
// upgraded (or whose trial started) gets it back without anyone clearing anything.

// RecheckAfter is how long a 402 hides a feature.
const RecheckAfter = 24 * time.Hour

// Features that a plan may lack (stored per workspace in sync_state).
const (
	Sections         = "sections"          // creating, deleting and reordering sections
	Tags             = "tags"              // renaming, recoloring and deleting tags
	MySection        = "my_section"        // the sections of My Tasks
	Search           = "search"            // the search API (premium only): typeahead is used instead
	Typeahead        = "typeahead"         // typeahead: names and tasks are then looked up in the cache only
	CustomID         = "custom_id"         // tasks by custom ID: only the gids found before are used
	TimeTracking     = "time_tracking"     // time tracking entries of tasks (#6)
	Goals            = "goals"             // goals and time periods (#7)
	Portfolios       = "portfolios"        // portfolios (#7)
	CustomFields     = "custom_fields"     // adding options to custom fields and custom fields to projects (#37)
	ProjectBrief     = "project_brief"     // a project's brief (preview API, #60)
	StartOn          = "start_on"          // start dates of tasks (#57)
	CustomTypes      = "custom_types"      // custom task types (#59)
	TaskTemplates    = "task_templates"    // making tasks from task templates (#2)
	ProjectTemplates = "project_templates" // making projects from project templates (#2)
)

// OffAt reports whether a 402 refusal recorded at refusedAt still hides its feature at
// now: for RecheckAfter. A zero refusedAt or one in the future (from a clock that was
// ahead) does not hide it, so the feature is tried again rather than hidden for good.
func OffAt(refusedAt, now time.Time) bool {
	if refusedAt.IsZero() {
		return false
	}
	age := now.Sub(refusedAt)
	return age >= 0 && age < RecheckAfter
}

// OffFor reports whether an RFC 3339 refusal timestamp still hides its feature at now.
// Invalid timestamps do not hide it, so corrupt records never lock a feature permanently.
func OffFor(value string, now time.Time) bool {
	at, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return false
	}
	return OffAt(at, now)
}

// Window returns [from, until) during which refusedAt hides a feature at now, and true if
// it is currently hidden.
func Window(refusedAt, now time.Time) (from, until time.Time, off bool) {
	if !OffAt(refusedAt, now) {
		return time.Time{}, time.Time{}, false
	}
	return refusedAt, refusedAt.Add(RecheckAfter), true
}

// FeatureOf returns the feature an outbox kind belongs to ("" for none).
func FeatureOf(kind string) string {
	switch kind {
	case outbox.KindCreateSection, outbox.KindDeleteSection, outbox.KindInsertSection:
		return Sections
	case outbox.KindUpdateTag, outbox.KindDeleteTag:
		return Tags
	case outbox.KindMoveMySection:
		return MySection
	case outbox.KindAddTimeEntry, outbox.KindUpdateTimeEntry, outbox.KindDeleteTimeEntry:
		return TimeTracking
	case outbox.KindCreateEnumOption, outbox.KindAddFieldSetting, outbox.KindRemoveFieldSetting:
		return CustomFields
	case outbox.KindUpdateProjectBrief:
		return ProjectBrief
	}
	return ""
}

// UnavailableMessage says that a feature cannot be used with the workspace's plan.
func UnavailableMessage(feature string) string {
	what := map[string]string{
		Sections:         "セクションの作成・削除・並べ替え",
		Tags:             "タグの名前・色の変更と削除",
		MySection:        "マイタスクのセクション",
		TimeTracking:     "タイムトラッキング (作業時間の記録)",
		Search:           "タスクの検索",
		Typeahead:        "名前での検索 (typeahead)",
		CustomID:         "タスクの ID (custom ID) での取得",
		Goals:            "ゴール",
		Portfolios:       "ポートフォリオ",
		CustomFields:     "カスタムフィールドの選択肢の追加とプロジェクトへの追加",
		ProjectBrief:     "プロジェクトブリーフ (概要のページ)",
		StartOn:          "タスクの開始日",
		CustomTypes:      "カスタムタイプ (タスクの種類とステータス)",
		TaskTemplates:    "タスクテンプレート",
		ProjectTemplates: "プロジェクトテンプレート",
	}[feature]
	// Every feature is tried again after RecheckAfter.
	return "このワークスペースのプランでは、" + what + "を API で使えません (Asana が 402 を返しました。1 日たったら、もう一度試します)"
}

// UnavailableError is returned for a feature that Asana refused because the workspace's
// plan lacks it. The message says which.
type UnavailableError struct{ Feature string }

func (e *UnavailableError) Error() string { return UnavailableMessage(e.Feature) }
