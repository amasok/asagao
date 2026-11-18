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
	// KeyEvents + project gid: the sync token for the project's changes (GET /events).
	KeyEvents = "events:"
	// KeyPlanUnavailable + a PlanFeature: when Asana refused that feature because the
	// workspace's plan lacks it (402). The feature is not offered while it is set.
	KeyPlanUnavailable = "plan_unavailable:"
	// KeyUserTaskList + workspace gid: the gid of my My Tasks list there.
	KeyUserTaskList = "user_task_list:"
	// KeyTeams + workspace gid: when the teams I am in and my favorite projects were last
	// fetched.
	KeyTeams = "teams:"
	// KeyTimeEntries + task gid: when the task's time tracking entries were last fetched.
	KeyTimeEntries = "time_entries:"
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
	// PlanTimeTracking is the time tracking entries of tasks (#6). Unlike the three above,
	// its 402 is recorded per workspace and for a while only (readmodel/plan.go).
	PlanTimeTracking = "time_tracking"
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
	}[feature]
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
