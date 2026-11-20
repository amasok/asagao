package store

import (
	"context"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// Unexported sync_state key prefixes and names. Existing values in store.db depend on these
// exact strings, so they must never change.
const (
	keyMeGID                 = "me_gid"
	keyLastSync              = "last_sync"
	keyLastFullSync          = "last_full_sync"
	keyLastPoll              = "last_poll"
	keyEvents                = "events:"
	keyPlanUnavailable       = "plan_unavailable:"
	keyCustomFields          = "custom_fields:"
	keyCustomTypes           = "custom_types:"
	keyUserTaskList          = "user_task_list:"
	keyWorkspaceCustomFields = "workspace_custom_fields:"
	keyFieldOptionsLocked    = "field_options_locked:"
	keyProjectFieldsLocked   = "project_fields_locked:"
	keyTeams                 = "teams:"
	keyTimeEntries           = "time_entries:"
	keyCustomID              = "custom_id:"
	keyProjectTasks          = "project_tasks:"
	keyMyTasks               = "my_tasks:"
	keyTaskOrder             = "task_order:"
	keySubtaskOrder          = "subtask_order"
	keyGoals                 = "goals:"
	keyPortfolios            = "portfolios:"
	keyCollapsed             = "ui:collapsed:"
	keyLayout                = "ui:layout"
)

// Unexported sync_state key prefixes mapping local GIDs to what Asana created.
const (
	resultPrefixTask       = "gid:"
	resultPrefixStory      = "story:"
	resultPrefixAttachment = "att:"
	resultPrefixStatus     = "status:"
	resultPrefixTag        = "tag:"
	resultPrefixSection    = "section:"
	resultPrefixTimeEntry  = "time:"
	resultPrefixEnumOption = "enum_option:"
)

func planKey(feature, workspaceGID string) string {
	return keyPlanUnavailable + feature + ":" + workspaceGID
}

func resultPrefix(kind readmodel.ResultKind) (string, error) {
	switch kind {
	case readmodel.ResultTask:
		return resultPrefixTask, nil
	case readmodel.ResultStory:
		return resultPrefixStory, nil
	case readmodel.ResultAttachment:
		return resultPrefixAttachment, nil
	case readmodel.ResultStatus:
		return resultPrefixStatus, nil
	case readmodel.ResultTag:
		return resultPrefixTag, nil
	case readmodel.ResultSection:
		return resultPrefixSection, nil
	case readmodel.ResultTimeEntry:
		return resultPrefixTimeEntry, nil
	case readmodel.ResultEnumOption:
		return resultPrefixEnumOption, nil
	}
	return "", fmt.Errorf("unknown result kind %q", kind)
}

func formatSyncTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

func (s *Store) syncTime(ctx context.Context, key string) (time.Time, error) {
	v, err := s.SyncState(ctx, key)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, nil // corrupt value: treat as never set
	}
	return t, nil
}

func (s *Store) setSyncTime(ctx context.Context, key string, at time.Time) error {
	return s.SetSyncState(ctx, key, formatSyncTime(at))
}

// --- 402 plan refusals ---

// PlanRefusedAt reports when Asana refused feature in workspaceGID with 402.
// Empty or corrupt timestamp values return zero, false, nil so corrupt records never
// permanently lock a feature.
func (s *Store) PlanRefusedAt(ctx context.Context, feature, workspaceGID string) (time.Time, bool, error) {
	v, err := s.SyncState(ctx, planKey(feature, workspaceGID))
	if err != nil || v == "" {
		return time.Time{}, false, err
	}
	at, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, false, nil
	}
	return at, true, nil
}

// RecordPlanRefusal records that Asana refused feature in workspaceGID at at.
func (s *Store) RecordPlanRefusal(ctx context.Context, feature, workspaceGID string, at time.Time) error {
	return s.setSyncTime(ctx, planKey(feature, workspaceGID), at)
}

// ClearPlanRefusal clears any recorded 402 refusal for feature in workspaceGID.
func (s *Store) ClearPlanRefusal(ctx context.Context, feature, workspaceGID string) error {
	key := planKey(feature, workspaceGID)
	v, err := s.SyncState(ctx, key)
	if err != nil || v == "" {
		return err
	}
	return s.SetSyncState(ctx, key, "")
}

// --- Account and sync timestamps ---

// SyncedMeGID returns the Asana user GID that the local data was synced with ("" if never synced).
func (s *Store) SyncedMeGID(ctx context.Context) (string, error) {
	return s.SyncState(ctx, keyMeGID)
}

// SetMeGID records the Asana user GID that the local data belongs to.
func (s *Store) SetMeGID(ctx context.Context, gid string) error {
	return s.SetSyncState(ctx, keyMeGID, gid)
}

func (s *Store) LastSyncAt(ctx context.Context) (time.Time, error) {
	return s.syncTime(ctx, keyLastSync)
}

func (s *Store) SetLastSyncAt(ctx context.Context, at time.Time) error {
	return s.setSyncTime(ctx, keyLastSync, at)
}

func (s *Store) LastFullSyncAt(ctx context.Context) (time.Time, error) {
	return s.syncTime(ctx, keyLastFullSync)
}

func (s *Store) SetLastFullSyncAt(ctx context.Context, at time.Time) error {
	return s.setSyncTime(ctx, keyLastFullSync, at)
}

func (s *Store) LastPollAt(ctx context.Context) (time.Time, error) {
	return s.syncTime(ctx, keyLastPoll)
}

func (s *Store) SetLastPollAt(ctx context.Context, at time.Time) error {
	return s.setSyncTime(ctx, keyLastPoll, at)
}

// --- Cursors and fetched timestamps ---

func (s *Store) EventsToken(ctx context.Context, projectGID string) (string, error) {
	return s.SyncState(ctx, keyEvents+projectGID)
}

func (s *Store) SetEventsToken(ctx context.Context, projectGID, token string) error {
	return s.SetSyncState(ctx, keyEvents+projectGID, token)
}

func (s *Store) SetUserTaskListGID(ctx context.Context, workspaceGID, listGID string) error {
	return s.SetSyncState(ctx, keyUserTaskList+workspaceGID, listGID)
}

func (s *Store) ProjectCustomFieldsFetchedAt(ctx context.Context, projectGID string) (string, error) {
	return s.SyncState(ctx, keyCustomFields+projectGID)
}

func (s *Store) SetProjectCustomFieldsFetchedAt(ctx context.Context, projectGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyCustomFields+projectGID, at)
}

func (s *Store) ProjectCustomTypesFetchedAt(ctx context.Context, projectGID string) (string, error) {
	return s.SyncState(ctx, keyCustomTypes+projectGID)
}

func (s *Store) SetProjectCustomTypesFetchedAt(ctx context.Context, projectGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyCustomTypes+projectGID, at)
}

func (s *Store) WorkspaceTeamsFetchedAt(ctx context.Context, workspaceGID string) (string, error) {
	return s.SyncState(ctx, keyTeams+workspaceGID)
}

func (s *Store) SetWorkspaceTeamsFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyTeams+workspaceGID, at)
}

func (s *Store) WorkspaceCustomFieldsFetchedAt(ctx context.Context, workspaceGID string) (string, error) {
	return s.SyncState(ctx, keyWorkspaceCustomFields+workspaceGID)
}

func (s *Store) SetWorkspaceCustomFieldsFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyWorkspaceCustomFields+workspaceGID, at)
}

func (s *Store) GoalsFetchedAt(ctx context.Context, workspaceGID string) (string, error) {
	return s.SyncState(ctx, keyGoals+workspaceGID)
}

func (s *Store) SetGoalsFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyGoals+workspaceGID, at)
}

func (s *Store) PortfoliosFetchedAt(ctx context.Context, workspaceGID string) (string, error) {
	return s.SyncState(ctx, keyPortfolios+workspaceGID)
}

func (s *Store) SetPortfoliosFetchedAt(ctx context.Context, workspaceGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyPortfolios+workspaceGID, at)
}

// --- 403 locks ---

func (s *Store) FieldOptionsLocked(ctx context.Context, fieldGID string) (bool, error) {
	v, err := s.SyncState(ctx, keyFieldOptionsLocked+fieldGID)
	return v != "", err
}

func (s *Store) SetFieldOptionsLocked(ctx context.Context, fieldGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyFieldOptionsLocked+fieldGID, at)
}

func (s *Store) ProjectFieldsLocked(ctx context.Context, projectGID string) (bool, error) {
	v, err := s.SyncState(ctx, keyProjectFieldsLocked+projectGID)
	return v != "", err
}

func (s *Store) SetProjectFieldsLocked(ctx context.Context, projectGID string, at time.Time) error {
	return s.setSyncTime(ctx, keyProjectFieldsLocked+projectGID, at)
}

// --- Custom ID cache ---

func (s *Store) CustomIDTaskGID(ctx context.Context, customID string) (string, error) {
	return s.SyncState(ctx, keyCustomID+customID)
}

func (s *Store) SetCustomIDTaskGID(ctx context.Context, customID, taskGID string) error {
	return s.SetSyncState(ctx, keyCustomID+customID, taskGID)
}

// --- Lazy sync and order retry state ---

func (s *Store) SetProjectTaskState(ctx context.Context, projectGID, state string) error {
	return s.SetSyncState(ctx, keyProjectTasks+projectGID, state)
}

func (s *Store) StoredProjectTaskStates(ctx context.Context) (map[string]string, error) {
	return s.SyncStatesWithPrefix(ctx, keyProjectTasks)
}

func (s *Store) MyTaskState(ctx context.Context, workspaceGID string) (string, error) {
	return s.SyncState(ctx, keyMyTasks+workspaceGID)
}

func (s *Store) MyTaskStates(ctx context.Context) (map[string]string, error) {
	return s.SyncStatesWithPrefix(ctx, keyMyTasks)
}

func (s *Store) SetMyTaskState(ctx context.Context, workspaceGID, state string) error {
	return s.SetSyncState(ctx, keyMyTasks+workspaceGID, state)
}

func (s *Store) PendingSectionOrders(ctx context.Context, projectGID string) (string, error) {
	return s.SyncState(ctx, keyTaskOrder+projectGID)
}

func (s *Store) SetPendingSectionOrders(ctx context.Context, projectGID, value string) error {
	return s.SetSyncState(ctx, keyTaskOrder+projectGID, value)
}

func (s *Store) PendingSubtaskOrders(ctx context.Context) (string, error) {
	return s.SyncState(ctx, keySubtaskOrder)
}

func (s *Store) SetPendingSubtaskOrders(ctx context.Context, value string) error {
	return s.SetSyncState(ctx, keySubtaskOrder, value)
}

// --- TUI UI state ---

func (s *Store) CollapsedGroups(ctx context.Context, scope string) (string, error) {
	return s.SyncState(ctx, keyCollapsed+scope)
}

func (s *Store) SetCollapsedGroups(ctx context.Context, scope, raw string) error {
	return s.SetSyncState(ctx, keyCollapsed+scope, raw)
}

func (s *Store) LayoutState(ctx context.Context) (string, error) {
	return s.SyncState(ctx, keyLayout)
}

func (s *Store) SetLayoutState(ctx context.Context, raw string) error {
	return s.SetSyncState(ctx, keyLayout, raw)
}
