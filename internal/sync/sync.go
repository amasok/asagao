// Package sync pulls data from Asana into the local store.
package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/plan"
)

const (
	// FullSyncInterval forces a full sync (which also removes deleted tasks) this often.
	FullSyncInterval = 24 * time.Hour
	// PollInterval is how often every project is fetched again with modified_since, besides
	// its events: Asana delivers events at most once, so a few may be missing. In between,
	// a sync only reads each project's events and fetches what they say changed.
	PollInterval = time.Hour
	// modifiedSinceMargin covers what the measured clock offset (see Syncer.serverNow) cannot:
	// its resolution, and a task saved in Asana with a modified_at just before the sync started.
	modifiedSinceMargin = 2 * time.Minute
)

// Config is what a sync keeps and fetches (the [sync] table of config.toml; a
// config.SyncConfig converts to it).
type Config struct {
	// CompletedDays is how many days of completed tasks to keep in the local store.
	CompletedDays int
	// AllProjects syncs every project in the workspace instead of only projects you are a member of.
	AllProjects bool
	// LazyProjects: see settings.SyncConfig.LazyProjects.
	LazyProjects bool
}

type Result struct {
	Full bool `json:"full"`
	// Poll: every project was fetched with modified_since (see PollInterval). Otherwise
	// projects were brought up to date from their events.
	Poll       bool       `json:"poll"`
	Events     int        `json:"events"`              // events read
	Fallbacks  int        `json:"fallbacks,omitempty"` // projects fetched again because their events could not be used
	Push       PushResult `json:"push"`
	Workspaces int        `json:"workspaces"`
	Projects   int        `json:"projects"` // projects whose tasks were synced
	// Listed is the projects only listed: their tasks are fetched once opened (#54).
	Listed int `json:"listed"`
	// CompletedPending: completed tasks of what was fetched for the first time are still to
	// fetch (Syncer.DeferCompleted): call FetchCompleted.
	CompletedPending bool `json:"completed_pending,omitempty"`
	// Requests is how many HTTP requests were sent to Asana (if the client counts them).
	Requests int64 `json:"requests"`
	Tasks    int   `json:"tasks"`
	Pruned   int64 `json:"pruned"`
	// OrderPending: sections and parents whose order (#51) Asana could not give: the
	// next sync lists them again.
	OrderPending int           `json:"order_pending,omitempty"`
	Duration     time.Duration `json:"duration_ns"`
}

// Run syncs Asana into the store.
//
// Most syncs read each project's events (GET /events) and fetch only the tasks they say
// changed (see events.go); My Tasks is fetched with modified_since. Every PollInterval, a
// poll also fetches every project with modified_since, which catches events Asana did not
// deliver; a project whose events cannot be used (expired token, 429, 5xx) is fetched
// that way too. A full sync fetches everything in the retention window and removes tasks
// Asana no longer returns. It runs when forced, on the first sync, or when the last one is
// older than FullSyncInterval.
//
// Only the projects to sync are fetched this way (see lazy.go): the others are listed, and
// their tasks are fetched once they are opened (OpenProject). What is fetched for the first
// time comes with its incomplete tasks first; the completed ones are fetched last.
func (s *Puller) Run(ctx context.Context, forceFull bool) (Result, error) {
	n := s.requests()
	res, err := s.run(ctx, forceFull)
	res.Requests = s.requests() - n
	return res, err
}

func (s *Puller) run(ctx context.Context, forceFull bool) (Result, error) {
	started := s.clock()
	res := Result{Full: forceFull}

	// Send local changes first, so the pull below sees them and a network error means
	// we are offline before anything else is attempted.
	// The store holds one account's data. If Asana now answers as someone else, nothing is
	// sent, fetched or overwritten: their 404s would look like deletions (see dropGone).
	me, err := s.API.Me(ctx)
	if err != nil {
		return res, fmt.Errorf("ユーザー情報の取得: %w", err)
	}
	if want, err := s.Store.SyncedMeGID(ctx); err != nil {
		return res, err
	} else if want != "" && me.GID != want {
		return res, fmt.Errorf("%w (%s / %s)", ErrAccountChanged, me.GID, want)
	} else if want != "" {
		s.Pusher.confirmAccount()
	}
	// The times recorded and compared below are by Asana's clock (measured from the Date
	// header of the answer to Me), as modified_since is compared with Asana's modified_at.
	stamp := started.Add(s.API.ClockOffset())
	lastSync, err := s.Store.LastSyncAt(ctx)
	if err != nil {
		return res, err
	}
	lastFull, err := s.Store.LastFullSyncAt(ctx)
	if err != nil {
		return res, err
	}
	if lastSync.IsZero() || lastFull.IsZero() || stamp.Sub(lastFull) >= FullSyncInterval {
		res.Full = true
	}
	// A stamp in the future by Asana's clock was written by the PC's clock (an older
	// version, or the clock was changed since): modified_since from it would miss changes,
	// and the next full sync would come late. Start again from a full sync. Less than the
	// margin is harmless (modified_since looks back by it) and may be noise of the offset.
	if limit := stamp.Add(modifiedSinceMargin); lastSync.After(limit) || lastFull.After(limit) {
		res.Full = true
	}
	lastPoll, err := s.Store.LastPollAt(ctx)
	if err != nil {
		return res, err
	}
	if lastPoll.IsZero() || lastPoll.After(lastSync) { // before events were used, every sync polled
		lastPoll = lastSync
	}
	res.Poll = res.Full || stamp.Sub(lastPoll) >= PollInterval
	// modifiedSince: what a project fetch asks for. A poll looks back to the previous poll
	// (events since then may be missing); otherwise to the previous sync (My Tasks, and
	// projects whose events cannot be used).
	var modifiedSince time.Time
	if !res.Full {
		modifiedSince = lastSync.Add(-modifiedSinceMargin)
		if res.Poll {
			modifiedSince = lastPoll.Add(-modifiedSinceMargin)
		}
	}
	// The whole sync holds the push lock: no other process sends meanwhile, and the store
	// cannot be reset (store.Reset) between our push and our pull, which would bring the
	// old data back. If another process is syncing or pushing, this sync is skipped.
	unlock, err := s.Pusher.lock(ctx, &res.Push)
	if err != nil {
		return res, err
	}
	defer unlock()
	var lastSent int64
	drops := &dropState{} // one account check and one drop limit for the whole sync
	if s.PullOnly {
		res.Push.Remaining, _, _, _ = s.Store.OutboxCounts(ctx)
	} else if res.Push, lastSent, err = s.Pusher.pushLocked(ctx, drops); err != nil {
		return res, fmt.Errorf("未送信の変更を送れませんでした: %w", err)
	}

	mode := s.Cfg.newMode(stamp)
	mode.full, mode.poll, mode.modifiedSince = res.Full, res.Poll, modifiedSince
	pl := newPull(&res)
	upsert := func(tasks []model.Task) error { return s.upsert(ctx, pl, tasks) }

	if err := s.Store.UpsertUser(ctx, *me); err != nil {
		return res, err
	}
	if err := s.Store.SetMeGID(ctx, me.GID); err != nil {
		return res, err
	}
	workspaces, err := s.API.Workspaces(ctx)
	if err != nil {
		return res, fmt.Errorf("ワークスペースの取得: %w", err)
	}
	if err := s.Store.UpsertWorkspaces(ctx, workspaces); err != nil {
		return res, err
	}
	if res.Full {
		if err := s.Store.PruneWorkspaces(ctx, workspaces); err != nil {
			return res, err
		}
	}
	res.Workspaces = len(workspaces)

	for _, w := range workspaces {
		if res.Poll { // members and tags change rarely: fetched with the poll
			if users, err := s.API.Users(ctx, w.GID); err == nil { // members, for choosing an assignee
				if err := s.Store.UpsertUsers(ctx, users); err != nil {
					return res, err
				}
			}
			if tags, err := s.API.Tags(ctx, w.GID); err == nil { // all tags, for the tag picker
				for i := range tags {
					if tags[i].Workspace == nil || tags[i].Workspace.GID == "" {
						tags[i].Workspace = &model.Ref{GID: w.GID}
					}
				}
				if err := s.Store.UpsertTags(ctx, tags); err != nil {
					return res, err
				}
			}
			if err := s.syncMySections(ctx, w.GID); err != nil {
				return res, err
			}
		}
		if err := s.syncTeamsAndFavorites(ctx, w, res.Poll); err != nil {
			return res, err
		}
		if err := s.syncWorkspaceCustomFields(ctx, w.GID, res.Poll); err != nil {
			return res, err
		}
		if err := s.pullMyTasks(ctx, pl, w, mode, lastSync.IsZero()); err != nil {
			return res, err
		}

		projects, err := s.API.Projects(ctx, w.GID, false)
		if err != nil {
			return res, fmt.Errorf("%s のプロジェクトの取得: %w", w.Name, err)
		}
		if err := s.Store.UpsertProjects(ctx, w.GID, projects, me.GID); err != nil {
			return res, err
		}
		if res.Full {
			if err := s.Store.PruneProjects(ctx, w.GID, projects); err != nil {
				return res, err
			}
		}
		// Read after the favorites were fetched: a new favorite is fetched by this sync.
		states, err := s.Store.ProjectTaskStates(ctx)
		if err != nil {
			return res, err
		}
		for _, p := range projects {
			if !s.toSync(p, me.GID, states) {
				res.Listed++
				continue
			}
			s.progress("プロジェクト %s", p.Name)
			if err := s.pullProject(ctx, pl, projectRef{p.GID, p.Name, w.GID}, states[p.GID], mode); err != nil {
				return res, err
			}
			res.Projects++
		}
	}
	if err := s.syncInboxes(ctx, workspaces); err != nil {
		return res, err
	}
	if st, err := s.Store.InboxState(ctx); err != nil {
		return res, err
	} else if st.Available() {
		if err := s.fetchNotificationTargets(ctx, pl.keep, res.Full, modifiedSince, upsert); err != nil {
			return res, err
		}
	}
	// The subtasks, then the events tokens and the tasks states (see finishPull).
	if err := s.finishPull(ctx, pl); err != nil {
		return res, err
	}
	// The completed tasks of what was fetched for the first time, after every incomplete
	// one (or later, by FetchCompleted).
	if s.DeferCompleted {
		if res.CompletedPending, err = s.completedPending(ctx); err != nil {
			return res, err
		}
	} else if err := s.pullCompleted(ctx, pl, mode, ""); err != nil {
		return res, err
	}

	// An account always has a workspace: an empty list is a broken answer, and pruning by
	// it would delete every task. The full sync is then done again next time.
	if res.Full && len(workspaces) > 0 {
		if res.Pruned, err = s.Store.PruneTasks(ctx, pl.keep); err != nil {
			return res, err
		}
		if err := s.Store.SetLastFullSyncAt(ctx, stamp); err != nil {
			return res, err
		}
	}
	if res.Poll {
		if err := s.Store.SetLastPollAt(ctx, stamp); err != nil {
			return res, err
		}
	}
	// Changes queued by other processes during the pull (they were told this sync sends them).
	if s.PullOnly {
		// Nothing is sent: they stay queued for a process that may push.
	} else if res.Push, _, err = s.Pusher.catchUp(ctx, res.Push, lastSent, drops); err != nil {
		return res, fmt.Errorf("未送信の変更を送れませんでした: %w", err)
	}
	if err := s.Store.SetLastSyncAt(ctx, stamp); err != nil {
		return res, err
	}
	// Mappings of things created offline to their Asana gids that nothing needs any more
	// (#44). Not an error for the sync: what is not pruned now is pruned next time.
	if _, err := s.Store.PruneResults(ctx, s.clock()); err != nil {
		s.progress("オフラインで作った項目の対応表の整理: %v", err)
	}
	res.Duration = s.clock().Sub(started)
	return res, nil
}

// syncCustomFields fetches a project's custom fields (with their choices) on a full sync,
// or if they were never fetched. A workspace without custom fields (Asana answers e.g. 402
// on a free plan) simply has none; any other failure is retried on the next sync.
func (s *Puller) syncCustomFields(ctx context.Context, projectGID string, full bool) error {
	if !full {
		if last, err := s.Store.ProjectCustomFieldsFetchedAt(ctx, projectGID); err != nil || last != "" {
			return err
		}
	}
	settings, err := s.API.CustomFieldSettings(ctx, projectGID)
	switch {
	case err == nil:
		if err := s.Store.ReplaceProjectCustomFields(ctx, projectGID, settings); err != nil {
			return err
		}
	case model.IsPaymentRequired(err) || isForbidden(err): // not available on this plan: no custom fields
		if err := s.Store.ReplaceProjectCustomFields(ctx, projectGID, nil); err != nil {
			return err
		}
	default:
		return nil // e.g. an expired token or a 5xx: keep what is cached, try again next time
	}
	return s.Store.SetProjectCustomFieldsFetchedAt(ctx, projectGID, s.clock())
}

// syncCustomTypes fetches the custom task types a project offers (#59), with their
// statuses, when its custom fields are fetched (syncCustomFields). A 402 (a plan without
// them) records it for the workspace (plan.go): no project of it asks again for a while,
// and it has no types meanwhile. Any other failure is retried on the next sync.
func (s *Puller) syncCustomTypes(ctx context.Context, projectGID, workspaceGID string, full bool) error {
	if !full {
		if last, err := s.Store.ProjectCustomTypesFetchedAt(ctx, projectGID); err != nil || last != "" {
			return err
		}
	}
	if hidden, err := planHidden(ctx, s.Store, s.clock(), plan.CustomTypes, workspaceGID); err != nil || hidden {
		return err // tried again on a full sync after the 402 is forgotten
	}
	types, err := s.API.CustomTypes(ctx, projectGID)
	if refused, recErr := recordPlanRefusal(ctx, s.Store, s.clock(), plan.CustomTypes, workspaceGID, err); refused || recErr != nil {
		if recErr != nil {
			return recErr
		}
		return s.Store.ReplaceProjectCustomTypes(ctx, projectGID, nil)
	}
	switch {
	case err == nil:
		if err := s.Store.ClearPlanRefusal(ctx, plan.CustomTypes, workspaceGID); err != nil {
			return err
		}
		if err := s.Store.ReplaceProjectCustomTypes(ctx, projectGID, types); err != nil {
			return err
		}
	case isForbidden(err):
		if err := s.Store.ReplaceProjectCustomTypes(ctx, projectGID, nil); err != nil {
			return err
		}
	default:
		return nil // e.g. an expired token or a 5xx: keep what is cached, try again next time
	}
	return s.Store.SetProjectCustomTypesFetchedAt(ctx, projectGID, s.clock())
}

// syncTeamsAndFavorites fetches the teams I am in and my favorite projects of a workspace
// (to group the project list and to choose a team for a new project). They change rarely:
// fetched with the poll, or if they were never fetched. A workspace that is not an
// organization has no teams. A failure keeps what is cached and is tried again on the
// next sync; only a local error stops the sync.
func (s *Puller) syncTeamsAndFavorites(ctx context.Context, w model.Workspace, poll bool) error {
	if !poll {
		if last, err := s.Store.WorkspaceTeamsFetchedAt(ctx, w.GID); err != nil || last != "" {
			return err
		}
	}
	done := true
	if w.IsOrganization {
		teams, err := s.API.MyTeams(ctx, w.GID)
		switch {
		case err == nil:
			if err := s.Store.ReplaceTeams(ctx, w.GID, teams); err != nil {
				return err
			}
		case model.IsPaymentRequired(err) || isForbidden(err):
			if err := s.Store.ReplaceTeams(ctx, w.GID, nil); err != nil {
				return err
			}
		default:
			done = false
		}
	}
	favorites, err := s.API.FavoriteProjects(ctx, w.GID)
	switch {
	case err == nil:
		if err := s.Store.ReplaceFavoriteProjects(ctx, w.GID, favorites); err != nil {
			return err
		}
	case model.IsPaymentRequired(err) || isForbidden(err):
		if err := s.Store.ReplaceFavoriteProjects(ctx, w.GID, nil); err != nil {
			return err
		}
	default:
		done = false
	}
	if !done { // tried again on the next sync
		return s.Store.SetWorkspaceTeamsFetchedAt(ctx, w.GID, time.Time{})
	}
	return s.Store.SetWorkspaceTeamsFetchedAt(ctx, w.GID, s.clock())
}

func isForbidden(err error) bool {
	var apiErr *model.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden
}

// FetchDetails refreshes one task with its subtasks and stories (comments and activity).
// Comments do not change a task's modified_at, so they are fetched on demand rather than by Run.
func (s *Details) FetchDetails(ctx context.Context, taskGID string) error {
	if localgid.IsLocal(taskGID) {
		return nil // created offline; nothing on the server yet
	}
	t, err := s.API.TaskDetail(ctx, taskGID)
	if err != nil {
		return err
	}
	subs, err := s.API.Subtasks(ctx, taskGID)
	if err != nil {
		return err
	}
	// Stories created after this (a comment sent meanwhile) are newer than the list (#40).
	// Asana's clock, as created_at, with a margin.
	storiesFrom := s.serverNow().Add(-clockSkew)
	stories, err := s.API.Stories(ctx, taskGID)
	if err != nil {
		return err
	}
	// Attachments are optional: if they cannot be read (e.g. the OAuth app lacks the
	// attachments:read scope), still store everything else and report the problem.
	atts, attErr := s.API.Attachments(ctx, taskGID)
	if err := s.Store.UpsertTasks(ctx, append([]model.Task{*t}, subs...)); err != nil {
		return err
	}
	if err := s.Store.SetSubtaskOrder(ctx, taskGID, taskGIDs(subs)); err != nil {
		return err
	}
	if attErr == nil {
		if err := s.Store.ReplaceAttachments(ctx, taskGID, atts); err != nil {
			return err
		}
	}
	if err := s.Store.ReplaceStories(ctx, taskGID, stories, storiesFrom); err != nil {
		return err
	}
	s.fetchDependencies(ctx, *t)
	if attErr != nil {
		return &AttachmentsError{Err: attErr}
	}
	return nil
}

// fetchDependencies stores the names and states of a task's dependencies and dependents,
// which may not be cached (the task itself only lists their gids). Without dependencies
// (e.g. a free plan) or offline, the task simply keeps what it has.
func (s *Details) fetchDependencies(ctx context.Context, t model.Task) {
	if len(t.Dependencies) == 0 && len(t.Dependents) == 0 {
		return
	}
	deps, err := s.API.Dependencies(ctx, t.GID)
	if err != nil {
		return
	}
	dependents, err := s.API.Dependents(ctx, t.GID)
	if err != nil {
		return
	}
	_ = s.Store.ReplaceDependencies(ctx, t.GID, deps, dependents)
}

// AttachmentsError means a task's details were refreshed except for its attachments.
type AttachmentsError struct{ Err error }

func (e *AttachmentsError) Error() string {
	return "添付ファイルを取得できませんでした: " + e.Err.Error()
}
func (e *AttachmentsError) Unwrap() error { return e.Err }
