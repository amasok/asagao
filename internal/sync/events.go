package sync

import (
	"context"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// Incremental sync from Asana's events (#27).
//
// Each member project has a sync token (store.KeyEvents + gid). GET /events with it returns
// what changed in the project since (its tasks and subtasks, sections, ...), 100 at a time.
// Only the tasks named there are fetched (POST /batch, 10 per request), so a sync with
// nothing changed costs one request per project. Asana answers 412 with a new token the
// first time and when the token has expired (about 24 hours); events are delivered at most
// once. Whenever the events cannot be used, the project is fetched with modified_since as
// before (see Run), and the hourly poll catches what was not delivered.

// maxEventPages bounds the events read per project and sync (100 each); the rest is read
// by the next sync.
const maxEventPages = 20

// projectEvents is what a project's events say needs fetching.
type projectEvents struct {
	token    string // to save once the changes are stored ("" = keep the old one)
	fallback bool   // the events cannot be used: fetch the project with modified_since
	// fresh: Asana gave the project its first token: fetch it in full once, as what
	// happened before the token is unknown. (Other failures fetch only what changed since
	// the last sync, so a project whose events keep failing does not cost a full fetch
	// every time.)
	fresh bool
	count int
	// tasks maps a changed task (or subtask) to whether it was added to the project or
	// undeleted: then its subtasks are fetched too, as they have no events of their own.
	tasks    map[string]bool
	order    []string
	sections bool // sections were added, renamed, moved or deleted
	fields   bool // the project's custom fields changed
	statuses bool // a status update was posted
}

// readEvents reads a project's events since its token. A failure of Asana (412, 429, 5xx,
// offline) is not an error: the result asks for a fallback fetch instead, which fails in
// turn if Asana cannot be reached.
func (s *Syncer) readEvents(ctx context.Context, projectGID string) (projectEvents, error) {
	ev := projectEvents{tasks: map[string]bool{}}
	token, err := s.Store.SyncState(ctx, store.KeyEvents+projectGID)
	if err != nil {
		return ev, err
	}
	hadToken := token != ""
	for page := 0; page < maxEventPages; page++ {
		p, err := s.API.Events(ctx, projectGID, token)
		if fresh, ok := asana.SyncToken(err); ok {
			// No token yet, or it expired: what changed before is unknown. The events read
			// so far (with the old token) are still applied; the fetch covers the rest.
			ev.token, ev.fallback, ev.fresh = fresh, true, !hadToken
			return ev, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ev, ctx.Err()
			}
			// Keep what was read (ev.token is the last good one, if any) and fetch the
			// project instead; the next sync goes on from the saved token.
			ev.fallback = true
			return ev, nil
		}
		ev.count += len(p.Events)
		for _, e := range p.Events {
			ev.note(projectGID, e)
		}
		token, ev.token = p.Sync, p.Sync
		if !p.HasMore {
			break
		}
	}
	return ev, nil
}

func (ev *projectEvents) note(projectGID string, e asana.Event) {
	switch e.Resource.ResourceType {
	case "task":
		if e.Resource.GID == "" {
			return
		}
		added := e.Action == "added" || e.Action == "undeleted"
		if _, seen := ev.tasks[e.Resource.GID]; !seen {
			ev.order = append(ev.order, e.Resource.GID)
		}
		ev.tasks[e.Resource.GID] = ev.tasks[e.Resource.GID] || added
	case "section":
		ev.sections = true
	case "custom_field", "custom_field_setting":
		ev.fields = true
	case "project_status", "status_update":
		ev.statuses = true
	case "project":
		if e.Resource.GID == projectGID && e.Change != nil && e.Change.Field == "custom_field_settings" {
			ev.fields = true
		}
	}
	// Stories (comments), attachments and likes do not change the task's own fields, and
	// are fetched when the task is opened.
}

// applyEvents fetches the tasks the events name that this sync has not fetched already,
// and removes those Asana no longer has or shows us. Unsent local changes keep winning
// (store.UpsertTasks reapplies them; a task with queued changes is not removed).
func (s *Syncer) applyEvents(ctx context.Context, ev projectEvents, keep map[string]bool,
	upsert func([]asana.Task) error, withSubtasks *[]string) error {
	var gids []string
	for _, gid := range ev.order {
		if !keep[gid] && !store.IsLocalGID(gid) {
			gids = append(gids, gid)
		}
	}
	if len(gids) == 0 {
		return nil
	}
	tasks, gone, err := s.API.TasksByGID(ctx, gids)
	if err != nil {
		return &eventFetchError{err}
	}
	before := len(*withSubtasks)
	if err := upsert(tasks); err != nil {
		return err
	}
	// upsert queued every fetched task with subtasks; only a task new to the project needs
	// them (its subtasks' changes are reported as events of their own).
	kept := (*withSubtasks)[:before]
	for _, gid := range (*withSubtasks)[before:] {
		if ev.tasks[gid] {
			kept = append(kept, gid)
		}
	}
	*withSubtasks = kept
	if _, err := s.Store.DeleteGoneTasks(ctx, gone); err != nil {
		return err
	}
	return nil
}

// eventFetchError means Asana could not give the tasks the events named (as opposed to
// an error of the local store). Run then fetches the project with modified_since.
type eventFetchError struct{ err error }

func (e *eventFetchError) Error() string { return e.err.Error() }
func (e *eventFetchError) Unwrap() error { return e.err }
