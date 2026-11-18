package readmodel

import (
	"path/filepath"

	"github.com/amasok/asagao/internal/domain/outbox"
	"regexp"
	"strings"
)

// Names of the local cache that the entry points (d4b) and sync (e1) use without importing
// the store. The store keeps aliases for them.

// Types of custom fields (CustomFieldView.Type) that asagao shows and edits.
const (
	CFText      = "text"
	CFNumber    = "number"
	CFEnum      = "enum"
	CFMultiEnum = "multi_enum"
	CFDate      = "date"
	CFPeople    = "people"
)

// Keys of the local cache's sync state that the entry points read or write.
const (
	// KeyMeGID is the gid of the user the local data was synced with ("" before the first
	// sync).
	KeyMeGID = "me_gid"
	// KeyCollapsed + scope: the TUI's collapsed groups of a list ("my", "inbox", or a
	// project gid).
	KeyCollapsed = "ui:collapsed:"
	// KeyLastSync is when the last sync finished (RFC 3339).
	KeyLastSync = "last_sync"
	// KeyCustomFields + project gid: when the project's custom fields were last fetched.
	KeyCustomFields = "custom_fields:"
	// KeyCustomTypes + project gid: when the custom task types the project offers were
	// last fetched (#59).
	KeyCustomTypes = "custom_types:"
	// KeyEvents + project gid: the sync token for the project's changes (GET /events).
	KeyEvents = "events:"
	// KeyPlanUnavailable + a PlanFeature: when Asana refused that feature because the
	// workspace's plan lacks it (402). The feature is not offered while it is set.
	KeyPlanUnavailable = "plan_unavailable:"
	// KeyUserTaskList + workspace gid: the gid of my My Tasks list there.
	KeyUserTaskList = "user_task_list:"
	// KeyWorkspaceCustomFields + workspace gid: when the workspace's custom fields were last
	// fetched (to add one to a project).
	KeyWorkspaceCustomFields = "workspace_custom_fields:"
	// KeyFieldOptionsLocked + field gid: when Asana refused to add an option to the field
	// (403: e.g. its options are locked, or not ours to change). Adding is not offered.
	KeyFieldOptionsLocked = "field_options_locked:"
	// KeyProjectFieldsLocked + project gid: when Asana refused to add or remove a custom
	// field of the project (403). It is not offered.
	KeyProjectFieldsLocked = "project_fields_locked:"
	// KeyTeams + workspace gid: when the teams I am in and my favorite projects were last
	// fetched.
	KeyTeams = "teams:"
	// KeyTimeEntries + task gid: when the task's time tracking entries were last fetched.
	KeyTimeEntries = "time_entries:"
	// KeyCustomID + a task's custom ID ("ENG-123"): the gid Asana gave for it, so that a
	// commit can name the task while offline once it was found (Issue #36).
	KeyCustomID = "custom_id:"
)

// Keys of the sync state for goals and portfolios (#7).
const (
	// KeyGoals + workspace gid: when the goals and time periods of the workspace were last
	// fetched.
	KeyGoals = "goals:"
	// KeyPortfolios + workspace gid: when my portfolios of the workspace were last fetched.
	KeyPortfolios = "portfolios:"
)

// States of a project's brief in the local cache (project_overviews.brief_state; "" is not
// fetched yet). Stored, so they must not change.
const (
	BriefNone        = "none"        // the project has no brief
	BriefUnavailable = "unavailable" // Asana does not give it (plan, preview API)
	BriefAvailable   = "available"
)

// Features that a plan may lack (KeyPlanUnavailable).
const (
	PlanSections  = "sections"   // creating, deleting and reordering sections
	PlanTags      = "tags"       // renaming, recoloring and deleting tags
	PlanMySection = "my_section" // the sections of My Tasks
	PlanSearch    = "search"     // the search API (premium only): typeahead is used instead
	PlanTypeahead = "typeahead"  // typeahead: names and tasks are then looked up in the cache only
	PlanCustomID  = "custom_id"  // tasks by custom ID: only the gids found before are used
)

// Features of #6 that a plan may lack (PlanKey).
const (
	// PlanTimeTracking is the time tracking entries of tasks (#6).
	PlanTimeTracking = "time_tracking"
)

// Features of #7 that a plan may lack (PlanKey).
const (
	PlanGoals      = "goals"      // goals and time periods
	PlanPortfolios = "portfolios" // portfolios
)

// Features of #37 that a plan may lack (PlanKey).
const (
	// PlanCustomFields: adding options to custom fields and custom fields to projects.
	PlanCustomFields = "custom_fields"
)

// Features of #60 that a plan may lack (PlanKey).
const (
	// PlanProjectBrief is a project's brief (a preview API): Asana answered 402 to reading
	// or writing one in the workspace.
	PlanProjectBrief = "project_brief"
)

// BriefForbiddenMessage says that Asana does not let us read or change one project's
// brief (it answered 403; BriefUnavailable).
const BriefForbiddenMessage = "このプロジェクトのブリーフは、Asana が読み書きを許していません (403。権限がないか、API がプレビューのため)。概要を開き直すと、もう一度確かめます"

// Features of #57 that a plan may lack (PlanKey).
const (
	// PlanStartOn is the start dates of tasks (start_on).
	PlanStartOn = "start_on"
)

// Features of #59 that a plan may lack (PlanKey).
const (
	// PlanCustomTypes is custom task types (Starter and above): their list and setting them.
	PlanCustomTypes = "custom_types"
)

// PlanFeatureOf returns the feature an outbox kind belongs to ("" for none).
func PlanFeatureOf(kind string) string {
	switch kind {
	case outbox.KindCreateSection, outbox.KindDeleteSection, outbox.KindInsertSection:
		return PlanSections
	case outbox.KindUpdateTag, outbox.KindDeleteTag:
		return PlanTags
	case outbox.KindMoveMySection:
		return PlanMySection
	case outbox.KindAddTimeEntry, outbox.KindUpdateTimeEntry, outbox.KindDeleteTimeEntry:
		return PlanTimeTracking
	case outbox.KindCreateEnumOption, outbox.KindAddFieldSetting, outbox.KindRemoveFieldSetting:
		return PlanCustomFields
	case outbox.KindUpdateProjectBrief:
		return PlanProjectBrief
	}
	return ""
}

// PlanUnavailableMessage says that a feature cannot be used with the workspace's plan.
func PlanUnavailableMessage(feature string) string {
	what := map[string]string{
		PlanSections:     "セクションの作成・削除・並べ替え",
		PlanTags:         "タグの名前・色の変更と削除",
		PlanMySection:    "マイタスクのセクション",
		PlanTimeTracking: "タイムトラッキング (作業時間の記録)",
		PlanSearch:       "タスクの検索",
		PlanTypeahead:    "名前での検索 (typeahead)",
		PlanCustomID:     "タスクの ID (custom ID) での取得",
		PlanGoals:        "ゴール",
		PlanPortfolios:   "ポートフォリオ",
		PlanCustomFields: "カスタムフィールドの選択肢の追加とプロジェクトへの追加",
		PlanProjectBrief: "プロジェクトブリーフ (概要のページ)",
		PlanStartOn:      "タスクの開始日",
		PlanCustomTypes:  "カスタムタイプ (タスクの種類とステータス)",
	}[feature]
	if feature == PlanProjectBrief { // tried again after PlanRecheckAfter
		return "このワークスペースのプランでは、" + what + "を API で使えません (Asana が 402 を返しました。1 日たったら、もう一度試します)"
	}
	return "このワークスペースのプランでは、" + what + "を API で使えません (Asana が 402 を返しました)"
}

// Prefixes of the sync_state keys that map a local gid to what Asana created for it (a
// prefix + the local gid; the value is the Asana gid).
const (
	ResultTask       = "gid:"
	ResultStory      = "story:"
	ResultAttachment = "att:"
	ResultStatus     = "status:"
	// ResultTag maps a tag created offline to the tag Asana created.
	ResultTag = "tag:"
	// ResultSection maps a section created offline to the section Asana created.
	ResultSection = "section:"
	// ResultTimeEntry maps a time tracking entry recorded offline to the one Asana created.
	ResultTimeEntry = "time:"
	// ResultEnumOption maps a custom field option created offline to the one Asana created.
	ResultEnumOption = "enum_option:"
)

// ResetResult says what a reset with a backup did.
type ResetResult struct {
	Unsent     int    `json:"unsent"`                // unsent changes deleted
	BackupPath string `json:"backup_path,omitempty"` // "" when there was nothing to keep
}

// AnyUnsent lets a reset delete every unsent change, however many there are.
const AnyUnsent int64 = -1

var imageExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true, ".heic": true, ".bmp": true}

// IsImageName reports whether a file name looks like an image.
func IsImageName(name string) bool { return imageExts[strings.ToLower(filepath.Ext(name))] }

// inlineAsset matches images pasted into task descriptions and comments, which the API
// returns as links like https://app.asana.com/app/asana/-/get_asset?asset_id=1220507679870013
// (the asset id is the attachment gid).
var inlineAsset = regexp.MustCompile(`https://app\.asana\.com/app/asana/-/get_asset\?[^\s)\]>]*?asset_id=(\d+)[^\s)\]>]*`)

// InlineAssets returns the attachment gids of images embedded in text, in order.
func InlineAssets(text string) []string {
	var gids []string
	for _, m := range inlineAsset.FindAllStringSubmatch(text, -1) {
		gids = append(gids, m[1])
	}
	return gids
}

// ReplaceInlineAssets replaces each embedded image link with repl(gid).
func ReplaceInlineAssets(text string, repl func(gid string) string) string {
	return inlineAsset.ReplaceAllStringFunc(text, func(link string) string {
		return repl(inlineAsset.FindStringSubmatch(link)[1])
	})
}
