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
	// FullSyncInterval forces a full sync (which also removes deleted tasks) this often.
	FullSyncInterval = 24 * time.Hour
	// modifiedSinceMargin absorbs clock skew between this machine and Asana.
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
	Full       bool          `json:"full"`
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

// Run syncs Asana into the store.
//
// An incremental sync fetches only tasks modified since the previous sync. A full sync
// fetches everything in the retention window and removes tasks Asana no longer returns.
// A full sync runs when forced, on the first sync, or when the last one is older than
// FullSyncInterval (incremental syncs cannot see deletions).
func (s *Syncer) Run(ctx context.Context, forceFull bool) (Result, error) {
	started := s.clock()
	res := Result{Full: forceFull}

	lastSync, err := s.stateTime(ctx, store.KeyLastSync)
	if err != nil {
		return res, err
	}
	lastFull, err := s.stateTime(ctx, keyLastFullSync)
	if err != nil {
		return res, err
	}
	if lastSync.IsZero() || lastFull.IsZero() || started.Sub(lastFull) >= FullSyncInterval {
		res.Full = true
	}
	var modifiedSince time.Time
	if !res.Full {
		modifiedSince = lastSync.Add(-modifiedSinceMargin)
	}
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

	completedSince := started.AddDate(0, 0, -s.Cfg.CompletedDays)
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
	res.Workspaces = len(workspaces)

	for _, w := range workspaces {
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
			secs, err := s.API.Sections(ctx, p.GID)
			if err != nil {
				return res, fmt.Errorf("%s のセクションの取得: %w", p.Name, err)
			}
			if err := s.Store.ReplaceSections(ctx, p.GID, secs); err != nil {
				return res, err
			}
			if err := s.syncCustomFields(ctx, p.GID, res.Full); err != nil {
				return res, err
			}
			if updates, err := s.API.StatusUpdates(ctx, p.GID); err == nil { // project status
				if err := s.Store.ReplaceStatusUpdates(ctx, p.GID, updates); err != nil {
					return res, err
				}
			}
			tasks, err := s.API.Tasks(ctx, asana.TaskQuery{
				Project: p.GID, CompletedSince: completedSince, ModifiedSince: modifiedSince,
			})
			if err != nil {
				return res, fmt.Errorf("%s のタスクの取得: %w", p.Name, err)
			}
			if err := upsert(tasks); err != nil {
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

	if res.Full {
		if res.Pruned, err = s.Store.PruneTasks(ctx, keep); err != nil {
			return res, err
		}
		if err := s.Store.SetSyncState(ctx, keyLastFullSync, started.UTC().Format(time.RFC3339)); err != nil {
			return res, err
		}
	}
	// Changes queued by other processes during the pull (they were told this sync sends them).
	if s.PullOnly {
		// Nothing is sent: they stay queued for a process that may push.
	} else if res.Push, _, err = s.catchUp(ctx, res.Push, lastSent, drops); err != nil {
		return res, fmt.Errorf("未送信の変更を送れませんでした: %w", err)
	}
	if err := s.Store.SetSyncState(ctx, store.KeyLastSync, started.UTC().Format(time.RFC3339)); err != nil {
		return res, err
	}
	res.Duration = s.clock().Sub(started)
	return res, nil
}

// keyCustomFields + project gid records when a project's custom fields were last fetched.
const keyCustomFields = "custom_fields:"

// syncCustomFields fetches a project's custom fields (with their choices) on a full sync,
// or if they were never fetched. A workspace without custom fields (Asana answers e.g. 402
// on a free plan) simply has none; any other failure is retried on the next sync.
func (s *Syncer) syncCustomFields(ctx context.Context, projectGID string, full bool) error {
	key := keyCustomFields + projectGID
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
