package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/readmodel"
)

// Projects fetched when they are opened (#54).
//
// A sync fetches the list of every project, but the tasks only of the projects to sync:
// those opened once (KeyProjectTasks), the favorites, those synced before #54 (they have
// an events token), and with Config.AllProjects every project (with LazyProjects false,
// every project I am a member of). Only those projects' events are read. A project opened
// for the first time is fetched by OpenProject (or, offline, by the next sync).
//
// The first fetch of a project, and of My Tasks, takes the incomplete tasks first
// (TasksOpen); the completed ones within CompletedDays come after every incomplete one
// (pullCompleted), as there may be many of them. Then the project is TasksAll and is kept
// up to date like before.

// pull is what one Run (or OpenProject, FetchCompleted) has fetched so far.
type pull struct {
	keep         map[string]bool   // tasks fetched (a full sync removes the others)
	withSubtasks []string          // fetched tasks whose subtasks are to be fetched
	subsFetched  map[string]bool   // tasks whose subtasks were fetched
	parents      []string          // parents of changed subtasks: their order may have changed (#51)
	tokens       map[string]string // project gid -> events token, saved once everything is stored
	states       map[string]string // sync_state key -> tasks state, saved with the tokens
	res          *Result
}

func newPull(res *Result) *pull {
	return &pull{keep: map[string]bool{}, subsFetched: map[string]bool{}, tokens: map[string]string{},
		states: map[string]string{}, res: res}
}

// pullMode is how a pull asks for tasks.
type pullMode struct {
	full, poll bool
	// split: what is fetched for the first time comes with its incomplete tasks first, its
	// completed ones after (Config.LazyProjects; without it, a sync works as before #54).
	split bool
	// modifiedSince is what a project fetch asks for (zero: everything); see Run.
	modifiedSince time.Time
	// stamp is when the sync started by Asana's clock: as completed_since it asks for the
	// incomplete tasks only.
	stamp time.Time
	// window is completed_since for the tasks kept: the incomplete ones, and those completed
	// within CompletedDays.
	window time.Time
}

func (s *Syncer) newMode(stamp time.Time) pullMode {
	days := max(s.Cfg.CompletedDays, 0) // a negative number would ask for a time to come
	return pullMode{stamp: stamp, window: stamp.AddDate(0, 0, -days), split: s.Cfg.LazyProjects}
}

// fetchedState is the state a first fetch with completed_since = stamp leaves: the
// completed tasks are still to come, unless none are kept.
func (m pullMode) fetchedState() string {
	if !m.split || !m.window.Before(m.stamp) {
		return readmodel.TasksAll
	}
	return readmodel.TasksOpen
}

func (s *Syncer) upsert(ctx context.Context, pl *pull, tasks []model.Task) error {
	for _, t := range tasks {
		pl.keep[t.GID] = true
		if t.NumSubtasks > 0 {
			pl.withSubtasks = append(pl.withSubtasks, t.GID)
		}
	}
	pl.res.Tasks += len(tasks)
	return s.Store.UpsertTasks(ctx, tasks)
}

// fetchSubtasks fetches the subtasks of the tasks queued in pl. Subtasks are not returned
// by GET /tasks?project=, so they are fetched per parent; upsert queues subtasks that have
// subtasks of their own, so nested levels are fetched too.
func (s *Syncer) fetchSubtasks(ctx context.Context, pl *pull) error {
	for i := 0; i < len(pl.withSubtasks); i++ {
		gid := pl.withSubtasks[i]
		if pl.subsFetched[gid] {
			continue
		}
		pl.subsFetched[gid] = true
		subs, err := s.API.Subtasks(ctx, gid)
		if err != nil {
			return fmt.Errorf("サブタスクの取得 (%s): %w", gid, err)
		}
		if err := s.storeSubtasks(ctx, pl, gid, subs); err != nil {
			return err
		}
	}
	pl.withSubtasks = pl.withSubtasks[:0]
	return nil
}

// storeSubtasks stores the subtasks of parent, all of them as Asana lists them, and
// their order (#51).
func (s *Syncer) storeSubtasks(ctx context.Context, pl *pull, parent string, subs []model.Task) error {
	if err := s.upsert(ctx, pl, subs); err != nil {
		return err
	}
	return s.Store.SetSubtaskOrder(ctx, parent, taskGIDs(subs))
}

// refreshParents lists the subtasks of the parents of subtasks the events named, and of
// those a previous sync could not list: their order may have changed, which only the
// parent's list shows (#51). The subtasks themselves were fetched already.
func (s *Syncer) refreshParents(ctx context.Context, pl *pull) error {
	pending, err := s.Store.SyncState(ctx, readmodel.KeySubtaskOrder)
	if err != nil {
		return err
	}
	parents := addNew(slices.Clone(pl.parents), strings.Fields(pending)...)
	pl.parents = pl.parents[:0]
	var left []string
	for _, gid := range parents {
		if pl.subsFetched[gid] || slices.Contains(pl.withSubtasks, gid) || localgid.IsLocal(gid) {
			continue // listed in full by this sync
		}
		gids, err := s.API.SubtaskOrder(ctx, gid)
		if ok, err := s.orderFailed(err, "サブタスクの並びの取得 ("+gid+")"); err != nil {
			return err
		} else if !ok {
			left = append(left, gid)
			continue
		}
		if gids != nil {
			if err := s.Store.SetSubtaskOrder(ctx, gid, gids); err != nil {
				return err
			}
		}
	}
	pl.res.OrderPending += len(left)
	return s.setPending(ctx, readmodel.KeySubtaskOrder, pending, left)
}

// refreshSections lists the order of the sections of project (#51) whose tasks changed
// without a listing of the whole project (listed: there was one): a task moved, added or
// reordered there. Sections a previous sync could not list are listed too.
func (s *Syncer) refreshSections(ctx context.Context, pl *pull, project string, sections []string,
	completedSince time.Time, listed bool) error {
	key := readmodel.KeyTaskOrder + project
	pending, err := s.Store.SyncState(ctx, key)
	if err != nil {
		return err
	}
	if !listed {
		sections = addNew(sections, strings.Fields(pending)...)
	}
	var left []string
	for _, gid := range sections {
		if localgid.IsLocal(gid) {
			continue
		}
		gids, err := s.API.SectionTaskOrder(ctx, gid, completedSince)
		if ok, err := s.orderFailed(err, "セクションの並びの取得 ("+gid+")"); err != nil {
			return err
		} else if !ok {
			left = append(left, gid)
			continue
		}
		if gids != nil {
			if err := s.Store.SetSectionTaskOrder(ctx, gid, gids); err != nil {
				return err
			}
		}
	}
	pl.res.OrderPending += len(left)
	return s.setPending(ctx, key, pending, left)
}

// orderFailed sorts out the error of a listing of the order. ok: done, or nothing to do
// (404, 403: the section or parent is gone, or not ours to see; the order known stays).
// Offline, or without a valid token, the sync fails as any other fetch. Otherwise (a 5xx
// or 429 that outlasted the retries, a 400, ...) the order is only how tasks are shown:
// the sync goes on, and the next one lists it again.
func (s *Syncer) orderFailed(err error, what string) (ok bool, _ error) {
	switch {
	case err == nil, isNotFound(err), isForbidden(err):
		return true, nil
	case isNetworkError(err), model.IsUnauthorized(err):
		return false, fmt.Errorf("%s: %w", what, err)
	}
	return false, nil
}

// setPending saves what is still to list under key, if it changed.
func (s *Syncer) setPending(ctx context.Context, key, before string, left []string) error {
	if v := strings.Join(left, " "); v != before {
		return s.Store.SetSyncState(ctx, key, v)
	}
	return nil
}

// addNew adds to list the gids not in it yet.
func addNew(list []string, gids ...string) []string {
	for _, g := range gids {
		if g != "" && !slices.Contains(list, g) {
			list = append(list, g)
		}
	}
	return list
}

// sectionsOf adds to sections those of project that tasks are in.
func sectionsOf(sections []string, project string, tasks []model.Task) []string {
	for _, t := range tasks {
		for _, m := range t.Memberships {
			if m.Project != nil && m.Project.GID == project && m.Section != nil && m.Section.GID != "" &&
				!slices.Contains(sections, m.Section.GID) {
				sections = append(sections, m.Section.GID)
			}
		}
	}
	return sections
}

func taskGIDs(tasks []model.Task) []string {
	gids := make([]string, len(tasks))
	for i, t := range tasks {
		gids[i] = t.GID
	}
	return gids
}

// finishPull fetches the subtasks still queued, then saves the events tokens and the tasks
// states: only now, after the subtasks of the tasks the events reported, so that if
// anything failed, the next sync reads the same events again.
func (s *Syncer) finishPull(ctx context.Context, pl *pull) error {
	if err := s.refreshParents(ctx, pl); err != nil {
		return err
	}
	if err := s.fetchSubtasks(ctx, pl); err != nil {
		return err
	}
	for gid, token := range pl.tokens {
		if err := s.Store.SetSyncState(ctx, readmodel.KeyEvents+gid, token); err != nil {
			return err
		}
	}
	for key, state := range pl.states {
		if err := s.Store.SetSyncState(ctx, key, state); err != nil {
			return err
		}
	}
	clear(pl.tokens)
	clear(pl.states)
	return nil
}

// toSync reports whether Run fetches a project's tasks (see the top of this file). states
// is Store.ProjectTaskStates.
func (s *Syncer) toSync(p model.Project, meGID string, states map[string]string) bool {
	if _, ok := states[p.GID]; ok || s.Cfg.AllProjects {
		return true
	}
	return !s.Cfg.LazyProjects && slices.ContainsFunc(p.Members, func(r model.Ref) bool { return r.GID == meGID })
}

// projectRef is a project as a pull needs it.
type projectRef struct{ gid, name, workspace string }

// pullProject brings a project's tasks up to date: from its events, or by fetching it
// (on a poll, when the events cannot be used, and the first time). state is the project's
// tasks state (readmodel.Tasks*, or "" if never fetched).
func (s *Syncer) pullProject(ctx context.Context, pl *pull, p projectRef, state string, mode pullMode) error {
	ev, err := s.readEvents(ctx, p.gid)
	if err != nil {
		return err
	}
	pl.res.Events += ev.count
	// Without usable events, the project is fetched like a poll: in full the first time
	// (nothing is known before the new token), else what changed since the last sync.
	fetch, since := mode.poll, mode.modifiedSince
	if ev.fallback {
		pl.res.Fallbacks++
		fetch = true
		if ev.fresh {
			since = time.Time{}
		}
	}
	first := state != readmodel.TasksOpen && state != readmodel.TasksAll
	if first {
		fetch, since = true, time.Time{}
	}
	// Until the project is TasksAll, only its incomplete tasks are asked for: the completed
	// ones come after every project's incomplete ones (pullCompleted).
	completedSince := mode.window
	if mode.split && state != readmodel.TasksAll {
		completedSince = mode.stamp
	}
	sections := func() error {
		secs, err := s.API.Sections(ctx, p.gid)
		if err != nil {
			return fmt.Errorf("%s のセクションの取得: %w", p.name, err)
		}
		return s.Store.ReplaceSections(ctx, p.gid, secs)
	}
	statuses := func() error {
		if updates, err := s.API.StatusUpdates(ctx, p.gid); err == nil { // project status
			return s.Store.ReplaceStatusUpdates(ctx, p.gid, updates)
		}
		return nil
	}
	// touched: the sections whose order may have changed, and was not listed (#51);
	// listed: the whole project was.
	var touched []string
	var listed bool
	fetchProject := func(since time.Time) error {
		if err := sections(); err != nil {
			return err
		}
		if err := statuses(); err != nil {
			return err
		}
		tasks, err := s.API.Tasks(ctx, model.TaskQuery{
			Project: p.gid, CompletedSince: completedSince, ModifiedSince: since,
		})
		if err != nil {
			return fmt.Errorf("%s のタスクの取得: %w", p.name, err)
		}
		if err := s.upsert(ctx, pl, tasks); err != nil {
			return err
		}
		if since.IsZero() { // every task kept, in the project's order
			listed = true
			return s.Store.SetProjectTaskOrder(ctx, p.gid, taskGIDs(tasks))
		}
		touched = sectionsOf(touched, p.gid, tasks)
		return nil
	}
	if fetch {
		if err := fetchProject(since); err != nil {
			return err
		}
	} else {
		if ev.sections {
			if err := sections(); err != nil {
				return err
			}
		}
		if ev.statuses {
			if err := statuses(); err != nil {
				return err
			}
		}
	}
	if err := s.syncCustomFields(ctx, p.gid, mode.full || ev.fields); err != nil {
		return err
	}
	if err := s.syncCustomTypes(ctx, p.gid, p.workspace, mode.full || ev.fields); err != nil {
		return err
	}
	upsert := func(tasks []model.Task) error {
		touched = sectionsOf(touched, p.gid, tasks)
		for _, t := range tasks {
			if t.Parent != nil {
				pl.parents = addNew(pl.parents, t.Parent.GID)
			}
		}
		return s.upsert(ctx, pl, tasks)
	}
	if err := s.applyEvents(ctx, ev, pl.keep, upsert, &pl.withSubtasks); err != nil {
		var fe *eventFetchError
		if !errors.As(err, &fe) || isNetworkError(err) || model.IsUnauthorized(err) {
			return fmt.Errorf("%s の変更の取得: %w", p.name, err)
		}
		// Asana could not give the changed tasks (a 5xx or 429 that outlasted the
		// retries, a task that always fails): like unusable events, fetch the project
		// with modified_since instead, which covers the changes the events named.
		if !fetch {
			pl.res.Fallbacks++
			if err := fetchProject(mode.modifiedSince); err != nil {
				return err
			}
		}
	}
	if err := s.refreshSections(ctx, pl, p.gid, touched, completedSince, listed); err != nil {
		return err
	}
	if ev.token != "" {
		pl.tokens[p.gid] = ev.token
	}
	if first {
		pl.states[readmodel.KeyProjectTasks+p.gid] = mode.fetchedState()
	}
	return nil
}

// pullMyTasks fetches My Tasks of a workspace. My Tasks may hold tasks in no project, or in
// projects not synced, which no events report: it is fetched with modified_since on every
// sync (one request, only what changed). The first time, only the incomplete tasks are
// asked for (the completed ones come with pullCompleted). firstSync: no sync finished
// before (otherwise My Tasks without a state was fetched in full before #54).
func (s *Syncer) pullMyTasks(ctx context.Context, pl *pull, w model.Workspace, mode pullMode, firstSync bool) error {
	key := readmodel.KeyMyTasks + w.GID
	state, err := s.Store.SyncState(ctx, key)
	if err != nil {
		return err
	}
	if state == "" && (!firstSync || !mode.split) {
		state = readmodel.TasksAll // fetched in full before #54, or fetched as before
	}
	q := model.TaskQuery{Assignee: "me", Workspace: w.GID, CompletedSince: mode.window, ModifiedSince: mode.modifiedSince}
	if mode.split && state != readmodel.TasksAll {
		q.CompletedSince = mode.stamp
		if state != readmodel.TasksOpen {
			q.ModifiedSince = time.Time{}
			pl.states[key] = mode.fetchedState()
		}
	}
	s.progress("ワークスペース %s: 自分のタスク", w.Name)
	mine, err := s.API.Tasks(ctx, q)
	if err != nil {
		return fmt.Errorf("%s の担当タスクの取得: %w", w.Name, err)
	}
	return s.upsert(ctx, pl, mine)
}

// pullCompleted fetches the completed tasks (within CompletedDays) of My Tasks and of the
// projects whose incomplete tasks only are cached (TasksOpen), with the subtasks of the
// completed ones, and records each TasksAll. only, if not "", limits it to that project.
// It is called after finishPull (the states it reads are saved).
func (s *Syncer) pullCompleted(ctx context.Context, pl *pull, mode pullMode, only string) error {
	type scope struct {
		key, name string
		q         model.TaskQuery
	}
	// A task completed since window was modified since then too: modified_since leaves out
	// the incomplete tasks (fetched already) that did not change meanwhile.
	since := mode.window
	var scopes []scope
	if only == "" {
		my, err := s.Store.SyncStatesWithPrefix(ctx, readmodel.KeyMyTasks)
		if err != nil {
			return err
		}
		for _, ws := range sortedKeys(my) {
			if my[ws] == readmodel.TasksOpen {
				scopes = append(scopes, scope{readmodel.KeyMyTasks + ws, "自分のタスク",
					model.TaskQuery{Assignee: "me", Workspace: ws, CompletedSince: since, ModifiedSince: since}})
			}
		}
	}
	projects, err := s.Store.SyncStatesWithPrefix(ctx, readmodel.KeyProjectTasks)
	if err != nil {
		return err
	}
	for _, gid := range sortedKeys(projects) {
		if projects[gid] != readmodel.TasksOpen || (only != "" && gid != only) {
			continue
		}
		name := gid
		if ref, err := s.Store.ProjectRef(ctx, gid); err == nil && ref.Name != "" {
			name = ref.Name
		}
		scopes = append(scopes, scope{readmodel.KeyProjectTasks + gid, name,
			model.TaskQuery{Project: gid, CompletedSince: since, ModifiedSince: since}})
	}
	for _, sc := range scopes {
		s.progress("完了済みのタスク: %s", sc.name)
		tasks, err := s.API.Tasks(ctx, sc.q)
		if err != nil {
			return fmt.Errorf("%s の完了済みタスクの取得: %w", sc.name, err)
		}
		before := len(pl.withSubtasks)
		if err := s.upsert(ctx, pl, tasks); err != nil {
			return err
		}
		// The completed tasks have their place among the others (#51).
		if sc.q.Project != "" {
			if err := s.refreshSections(ctx, pl, sc.q.Project, sectionsOf(nil, sc.q.Project, tasks), since, false); err != nil {
				return err
			}
		}
		// The incomplete tasks' subtasks came with them: only the completed ones' are new.
		completed := map[string]bool{}
		for _, t := range tasks {
			completed[t.GID] = t.Completed
		}
		queued := pl.withSubtasks[:before]
		for _, gid := range pl.withSubtasks[before:] {
			if completed[gid] {
				queued = append(queued, gid)
			}
		}
		pl.withSubtasks = queued
		if err := s.fetchSubtasks(ctx, pl); err != nil {
			return err
		}
		if err := s.Store.SetSyncState(ctx, sc.key, readmodel.TasksAll); err != nil {
			return err
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// completedPending reports whether some completed tasks are still to fetch (pullCompleted).
func (s *Syncer) completedPending(ctx context.Context) (bool, error) {
	for _, prefix := range []string{readmodel.KeyMyTasks, readmodel.KeyProjectTasks} {
		states, err := s.Store.SyncStatesWithPrefix(ctx, prefix)
		if err != nil {
			return false, err
		}
		for _, st := range states {
			if st == readmodel.TasksOpen {
				return true, nil
			}
		}
	}
	return false, nil
}

// OpenResult is what OpenProject did.
type OpenResult struct {
	// State is the project's tasks state afterwards (readmodel.Tasks*).
	State string `json:"state"`
	// Fetched: tasks were fetched now (otherwise they were cached already).
	Fetched bool `json:"fetched"`
	// Tasks is how many tasks (and subtasks) were fetched, each counted once.
	Tasks    int   `json:"tasks"`
	Requests int64 `json:"requests"`
}

// OpenProject is called when a project is opened (selected in the TUI, named to list its
// tasks by the CLI or an MCP tool). It records the project as one to sync (so the next sync
// fetches it, if this cannot) and, if its tasks are not cached yet, fetches its incomplete
// tasks; withCompleted fetches the completed ones (CompletedDays) too, else they are left
// to FetchCompleted or the next sync. It only fetches: queued local changes stay queued
// (and are not overwritten). It takes the push lock: while another sync runs it returns
// readmodel.ErrPushBusy, and that sync, or the next, fetches the project.
func (s *Syncer) OpenProject(ctx context.Context, projectGID string, withCompleted bool) (out OpenResult, err error) {
	if localgid.IsLocal(projectGID) {
		out.State = readmodel.TasksAll // created offline: nothing on the server yet
		return out, nil
	}
	st, err := s.Store.WantProject(ctx, projectGID)
	if err != nil {
		return out, err
	}
	out.State = st
	if st == readmodel.TasksAll || (st == readmodel.TasksOpen && !withCompleted) {
		return out, nil
	}
	ref, err := s.Store.ProjectRef(ctx, projectGID)
	if err != nil {
		return out, err
	}
	n := s.requests()
	defer func() { out.Requests = s.requests() - n }()
	var res Result
	unlock, err := s.lock(ctx, &res.Push)
	if err != nil {
		return out, err
	}
	defer unlock()
	if err := s.checkPushAccount(ctx); err != nil {
		return out, err
	}
	// Another process may have fetched it meanwhile.
	if st, err = s.Store.ProjectTaskState(ctx, projectGID); err != nil {
		return out, err
	}
	pl := newPull(&res)
	mode := s.newMode(s.serverNow())
	if st != readmodel.TasksOpen && st != readmodel.TasksAll {
		s.progress("プロジェクト %s", ref.Name)
		if err := s.pullProject(ctx, pl, projectRef{ref.GID, ref.Name, ref.WorkspaceGID}, st, mode); err != nil {
			return out, err
		}
		if err := s.finishPull(ctx, pl); err != nil {
			return out, err
		}
		out.Fetched = true
	}
	if withCompleted {
		if err := s.pullCompleted(ctx, pl, mode, projectGID); err != nil {
			out.Tasks = len(pl.keep)
			return out, err
		}
		out.Fetched = true
	}
	out.Tasks = len(pl.keep) // each once (the completed ones' query returns changed incomplete ones too)
	out.State, err = s.Store.ProjectTaskState(ctx, projectGID)
	return out, err
}

// FetchCompleted fetches the completed tasks that Run left (Syncer.DeferCompleted,
// Result.CompletedPending): those of My Tasks and the projects fetched for the first time.
// Like OpenProject, it only fetches, under the push lock.
func (s *Syncer) FetchCompleted(ctx context.Context) (res Result, err error) {
	started := s.clock()
	n := s.requests()
	defer func() { res.Requests = s.requests() - n }()
	unlock, err := s.lock(ctx, &res.Push)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := s.checkPushAccount(ctx); err != nil {
		return res, err
	}
	pl := newPull(&res)
	err = s.pullCompleted(ctx, pl, s.newMode(s.serverNow()), "")
	res.Duration = s.clock().Sub(started)
	return res, err
}

// requests is how many HTTP requests the API client has sent so far, if it counts them.
func (s *Syncer) requests() int64 {
	if c, ok := s.API.(interface{ Requests() int64 }); ok {
		return c.Requests()
	}
	return 0
}
