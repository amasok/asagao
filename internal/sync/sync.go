// Package sync pulls data from Asana into the local store.
package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

const (
	keyLastFullSync = "last_full_sync"
	keyLastPoll     = "last_poll"
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

type Syncer struct {
	API   *asana.Client
	Store *store.Store
	Cfg   config.SyncConfig
	// Progress receives human-readable progress messages (optional).
	Progress func(format string, args ...any)
	// CacheDir is where attachments are downloaded (default: config.CacheDir()/attachments).
	CacheDir string
	// Download fetches a pre-signed attachment URL. It must not add the Asana
	// Authorization header (the storage service rejects it). Default: plain HTTP GET.
	Download func(ctx context.Context, url string) ([]byte, error)
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error // replaced in tests
	// PullOnly makes Run only fetch from Asana: queued local changes are left in the
	// outbox (they stay protected from being overwritten) and Push refuses to send.
	// Used by the read-only MCP server, so an agent cannot send what the user queued.
	PullOnly bool
	// accountOK is set once Asana answered as the account the local data belongs to, so
	// Push asks /users/me only once per Syncer (see checkPushAccount).
	accountOK atomic.Bool
}

// wait pauses for d (or until ctx is done).
func (s *Syncer) wait(ctx context.Context, d time.Duration) error {
	if s.sleep != nil {
		return s.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type Result struct {
	Full bool `json:"full"`
	// Poll: every project was fetched with modified_since (see PollInterval). Otherwise
	// projects were brought up to date from their events.
	Poll       bool          `json:"poll"`
	Events     int           `json:"events"`              // events read
	Fallbacks  int           `json:"fallbacks,omitempty"` // projects fetched again because their events could not be used
	Push       PushResult    `json:"push"`
	Workspaces int           `json:"workspaces"`
	Projects   int           `json:"projects"`
	Tasks      int           `json:"tasks"`
	Pruned     int64         `json:"pruned"`
	Duration   time.Duration `json:"duration_ns"`
}

func (s *Syncer) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// serverNow is the current time by Asana's clock (as far as the responses so far tell):
// what is compared with Asana's timestamps or sent as modified_since must not depend on
// this machine's clock being right.
func (s *Syncer) serverNow() time.Time {
	if s.API == nil {
		return s.clock()
	}
	return s.clock().Add(s.API.ClockOffset())
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
func (s *Syncer) Run(ctx context.Context, forceFull bool) (Result, error) {
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
	if want, err := s.Store.SyncState(ctx, store.KeyMeGID); err != nil {
		return res, err
	} else if want != "" && me.GID != want {
		return res, fmt.Errorf("%w (%s / %s)", ErrAccountChanged, me.GID, want)
	} else if want != "" {
		s.accountOK.Store(true)
	}
	// The times recorded and compared below are by Asana's clock (measured from the Date
	// header of the answer to Me), as modified_since is compared with Asana's modified_at.
	stamp := started.Add(s.API.ClockOffset())
	lastSync, err := s.stateTime(ctx, store.KeyLastSync)
	if err != nil {
		return res, err
	}
	lastFull, err := s.stateTime(ctx, keyLastFullSync)
	if err != nil {
		return res, err
	}
	if lastSync.IsZero() || lastFull.IsZero() || stamp.Sub(lastFull) >= FullSyncInterval {
		res.Full = true
	}
	// A stamp in the future by Asana's clock was written by the PC's clock (an older
	// version, or the clock was changed since): modified_since from it would miss changes,
	// and the next full sync would come late. Start again from a full sync.
	if lastSync.After(stamp) || lastFull.After(stamp) {
		res.Full = true
	}
	lastPoll, err := s.stateTime(ctx, keyLastPoll)
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
	unlock, err := s.lock(ctx, &res.Push)
	if err != nil {
		return res, err
	}
	defer unlock()
	var lastSent int64
	drops := &dropState{} // one account check and one drop limit for the whole sync
	if s.PullOnly {
		res.Push.Remaining, _, _, _ = s.Store.OutboxCounts(ctx)
	} else if res.Push, lastSent, err = s.pushLocked(ctx, drops); err != nil {
		return res, fmt.Errorf("未送信の変更を送れませんでした: %w", err)
	}

	completedSince := stamp.AddDate(0, 0, -s.Cfg.CompletedDays)
	keep := map[string]bool{}
	var withSubtasks []string

	upsert := func(tasks []asana.Task) error {
		for _, t := range tasks {
			keep[t.GID] = true
			if t.NumSubtasks > 0 {
				withSubtasks = append(withSubtasks, t.GID)
			}
		}
		res.Tasks += len(tasks)
		return s.Store.UpsertTasks(ctx, tasks)
	}

	if err := s.Store.UpsertUser(ctx, *me); err != nil {
		return res, err
	}
	if err := s.Store.SetSyncState(ctx, store.KeyMeGID, me.GID); err != nil {
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
				if err := s.Store.UpsertTags(ctx, tags); err != nil {
					return res, err
				}
			}
		}
		// My Tasks may hold tasks in no project, which no project's events report: they are
		// fetched with modified_since on every sync (one request, only what changed).
		s.progress("ワークスペース %s: 自分のタスク", w.Name)
		mine, err := s.API.Tasks(ctx, asana.TaskQuery{
			Assignee: "me", Workspace: w.GID, CompletedSince: completedSince, ModifiedSince: modifiedSince,
		})
		if err != nil {
			return res, fmt.Errorf("%s の担当タスクの取得: %w", w.Name, err)
		}
		if err := upsert(mine); err != nil {
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
		for _, p := range projects {
			if !s.Cfg.AllProjects && !slices.ContainsFunc(p.Members, func(r asana.Ref) bool { return r.GID == me.GID }) {
				continue
			}
			s.progress("プロジェクト %s", p.Name)
			ev, err := s.readEvents(ctx, p.GID)
			if err != nil {
				return res, err
			}
			res.Events += ev.count
			// Without usable events, the project is fetched like a poll: in full the first time
			// (nothing is known before the new token), else what changed since the last sync.
			fetch, since := res.Poll, modifiedSince
			if ev.fallback {
				res.Fallbacks++
				fetch = true
				if ev.fresh {
					since = time.Time{}
				}
			}
			sections := func() error {
				secs, err := s.API.Sections(ctx, p.GID)
				if err != nil {
					return fmt.Errorf("%s のセクションの取得: %w", p.Name, err)
				}
				return s.Store.ReplaceSections(ctx, p.GID, secs)
			}
			statuses := func() error {
				if updates, err := s.API.StatusUpdates(ctx, p.GID); err == nil { // project status
					return s.Store.ReplaceStatusUpdates(ctx, p.GID, updates)
				}
				return nil
			}
			fetchProject := func(since time.Time) error {
				if err := sections(); err != nil {
					return err
				}
				if err := statuses(); err != nil {
					return err
				}
				tasks, err := s.API.Tasks(ctx, asana.TaskQuery{
					Project: p.GID, CompletedSince: completedSince, ModifiedSince: since,
				})
				if err != nil {
					return fmt.Errorf("%s のタスクの取得: %w", p.Name, err)
				}
				return upsert(tasks)
			}
			if fetch {
				if err := fetchProject(since); err != nil {
					return res, err
				}
			} else {
				if ev.sections {
					if err := sections(); err != nil {
						return res, err
					}
				}
				if ev.statuses {
					if err := statuses(); err != nil {
						return res, err
					}
				}
			}
			if err := s.syncCustomFields(ctx, p.GID, res.Full || ev.fields); err != nil {
				return res, err
			}
			if err := s.applyEvents(ctx, ev, keep, upsert, &withSubtasks); err != nil {
				var fe *eventFetchError
				if !errors.As(err, &fe) || isNetworkError(err) || asana.IsUnauthorized(err) {
					return res, fmt.Errorf("%s の変更の取得: %w", p.Name, err)
				}
				// Asana could not give the changed tasks (a 5xx or 429 that outlasted the
				// retries, a task that always fails): like unusable events, fetch the project
				// with modified_since instead, which covers the changes the events named.
				if !fetch {
					res.Fallbacks++
					if err := fetchProject(modifiedSince); err != nil {
						return res, err
					}
				}
			}
			// Saved only now: if anything above failed, the next sync reads the same events.
			if ev.token != "" {
				if err := s.Store.SetSyncState(ctx, store.KeyEvents+p.GID, ev.token); err != nil {
					return res, err
				}
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
		if err := s.fetchNotificationTargets(ctx, keep, res.Full, modifiedSince, upsert); err != nil {
			return res, err
		}
	}

	// Subtasks are not returned by GET /tasks?project=, so fetch them per parent.
	// upsert appends subtasks that have subtasks of their own, so nested levels are fetched too.
	fetched := map[string]bool{}
	for i := 0; i < len(withSubtasks); i++ {
		gid := withSubtasks[i]
		if fetched[gid] {
			continue
		}
		fetched[gid] = true
		subs, err := s.API.Subtasks(ctx, gid)
		if err != nil {
			return res, fmt.Errorf("サブタスクの取得 (%s): %w", gid, err)
		}
		if err := upsert(subs); err != nil {
			return res, err
		}
	}

	// An account always has a workspace: an empty list is a broken answer, and pruning by
	// it would delete every task. The full sync is then done again next time.
	if res.Full && len(workspaces) > 0 {
		if res.Pruned, err = s.Store.PruneTasks(ctx, keep); err != nil {
			return res, err
		}
		if err := s.Store.SetSyncState(ctx, keyLastFullSync, stamp.UTC().Format(time.RFC3339)); err != nil {
			return res, err
		}
	}
	if res.Poll {
		if err := s.Store.SetSyncState(ctx, keyLastPoll, stamp.UTC().Format(time.RFC3339)); err != nil {
			return res, err
		}
	}
	// Changes queued by other processes during the pull (they were told this sync sends them).
	if s.PullOnly {
		// Nothing is sent: they stay queued for a process that may push.
	} else if res.Push, _, err = s.catchUp(ctx, res.Push, lastSent, drops); err != nil {
		return res, fmt.Errorf("未送信の変更を送れませんでした: %w", err)
	}
	if err := s.Store.SetSyncState(ctx, store.KeyLastSync, stamp.UTC().Format(time.RFC3339)); err != nil {
		return res, err
	}
	res.Duration = s.clock().Sub(started)
	return res, nil
}

// syncCustomFields fetches a project's custom fields (with their choices) on a full sync,
// or if they were never fetched. A workspace without custom fields (Asana answers e.g. 402
// on a free plan) simply has none; any other failure is retried on the next sync.
func (s *Syncer) syncCustomFields(ctx context.Context, projectGID string, full bool) error {
	key := store.KeyCustomFields + projectGID
	if !full {
		if last, err := s.Store.SyncState(ctx, key); err != nil || last != "" {
			return err
		}
	}
	settings, err := s.API.CustomFieldSettings(ctx, projectGID)
	switch {
	case err == nil:
		if err := s.Store.ReplaceProjectCustomFields(ctx, projectGID, settings); err != nil {
			return err
		}
	case asana.IsPaymentRequired(err) || isForbidden(err): // not available on this plan: no custom fields
		if err := s.Store.ReplaceProjectCustomFields(ctx, projectGID, nil); err != nil {
			return err
		}
	default:
		return nil // e.g. an expired token or a 5xx: keep what is cached, try again next time
	}
	return s.Store.SetSyncState(ctx, key, s.clock().UTC().Format(time.RFC3339))
}

func isForbidden(err error) bool {
	var apiErr *asana.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden
}

// FetchDetails refreshes one task with its subtasks and stories (comments and activity).
// Comments do not change a task's modified_at, so they are fetched on demand rather than by Run.
func (s *Syncer) FetchDetails(ctx context.Context, taskGID string) error {
	if store.IsLocalGID(taskGID) {
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
	stories, err := s.API.Stories(ctx, taskGID)
	if err != nil {
		return err
	}
	// Attachments are optional: if they cannot be read (e.g. the OAuth app lacks the
	// attachments:read scope), still store everything else and report the problem.
	atts, attErr := s.API.Attachments(ctx, taskGID)
	if err := s.Store.UpsertTasks(ctx, append([]asana.Task{*t}, subs...)); err != nil {
		return err
	}
	if attErr == nil {
		if err := s.Store.ReplaceAttachments(ctx, taskGID, atts); err != nil {
			return err
		}
	}
	if err := s.Store.ReplaceStories(ctx, taskGID, stories, s.clock()); err != nil {
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
func (s *Syncer) fetchDependencies(ctx context.Context, t asana.Task) {
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

func (s *Syncer) stateTime(ctx context.Context, key string) (time.Time, error) {
	v, err := s.Store.SyncState(ctx, key)
	if err != nil || v == "" {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, nil // corrupt value: treat as never synced
	}
	return t, nil
}

func (s *Syncer) progress(format string, args ...any) {
	if s.Progress != nil {
		s.Progress(format, args...)
	}
}
