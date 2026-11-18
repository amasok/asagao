package sync

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// Sending several entries in one request (#28).
//
// Changes to many tasks at once (e.g. completing or moving the tasks selected in the TUI)
// queue one entry per task. Entries next to each other in the queue that only change a
// task that already exists in Asana go out together, up to model.MaxBatchActions in one
// POST /batch: updates (fields, custom fields), moves to a section, and adding or removing
// a tag, a follower, a project or a dependency. Each of them is harmless to repeat, which
// matters because the client retries a batch that got a 5xx.
//
// Anything else ends the group and is sent on its own as before: creates, comments,
// attachments, status updates (later entries use their result), an entry to be checked in
// Asana first (verify), and an entry that refers to something created offline (a local
// gid: its create must go first). A group never holds two entries for the same task, so
// the changes to one task still reach Asana in the order they were made.
//
// What Asana answers to each request is handled like the answer to that entry sent on its
// own (pushEntry): conflicts, 404 (dropGone), 4xx, 429 and 401. A request it answers with
// 400 is sent again on its own, which gives the same answer with what it means (e.g. a
// custom field value in a form Asana does not take). When the batch as a whole fails
// (offline, 429, 401, a 5xx after the retries), every entry in it is handled as if its own
// request had failed so: Asana may or may not have applied it. A batch Asana refuses, or
// whose answer cannot be read, is sent again one entry at a time.

// batchEntry is an entry that can be sent in a POST /batch with others.
type batchEntry struct {
	op    outbox.Op // as re-read from the store
	task  string    // the task it changes (an Asana gid)
	write model.BatchWrite
	// check: compare with the task in Asana before sending (updates, custom fields).
	check bool
	// refresh: read the task again after sending (a move: the answer has no task).
	refresh bool
	// alone: send it on its own (it changed after the group was formed).
	alone bool
}

// batchGroup returns the entries at the start of ops (as listed when the pass began) that
// can be sent together, at most model.MaxBatchActions. Fewer than two are not worth a batch.
func (s *Syncer) batchGroup(ctx context.Context, ops []outbox.Op) ([]batchEntry, error) {
	var group []batchEntry
	tasks := map[string]bool{}
	for _, snapshot := range ops {
		if len(group) == model.MaxBatchActions {
			break
		}
		// Re-read like pushEntry: an earlier entry may have rewritten this one.
		op, err := s.Store.GetOp(ctx, snapshot.ID)
		if errors.Is(err, readmodel.ErrNotFound) {
			break
		}
		if err != nil {
			return nil, err
		}
		if op.State != outbox.StatePending || op.Verify || op.Kind != snapshot.Kind {
			break
		}
		e, ok, err := s.batchEntryFor(ctx, *op)
		if err != nil {
			return nil, err
		}
		if !ok || tasks[e.task] {
			break // sent on its own; a second change to a task waits for the first
		}
		tasks[e.task] = true
		group = append(group, e)
	}
	return group, nil
}

// batchEntryFor builds the request for op if it can go out in a batch (ok).
func (s *Syncer) batchEntryFor(ctx context.Context, op outbox.Op) (e batchEntry, ok bool, err error) {
	task := op.TaskGID
	if task == "" || localgid.IsLocal(task) {
		return e, false, nil
	}
	e = batchEntry{op: op, task: task}
	taskPath := "/tasks/" + url.PathEscape(task)
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	real := func(gid string) bool { return gid != "" && !localgid.IsLocal(gid) }
	switch op.Kind {
	case outbox.KindUpdateTask:
		e.write = model.BatchWrite{Method: http.MethodPut, Path: taskPath, Data: apiFields(op.Payload), Task: true}
		e.check = true
	case outbox.KindSetCustomField:
		if str("field") == "" {
			return e, false, nil
		}
		e.write = model.BatchWrite{Method: http.MethodPut, Path: taskPath, Data: customFieldUpdate(op), Task: true}
		e.check = true
	case outbox.KindMoveTask:
		section := str("section")
		if !real(section) {
			return e, false, nil
		}
		data := map[string]any{"task": task}
		for _, k := range []string{"insert_before", "insert_after"} {
			if anchor := str(k); anchor != "" {
				if anchor, err = s.Store.RealGID(ctx, anchor); err != nil {
					return e, false, err
				}
				if !real(anchor) {
					return e, false, nil // sent alone (pushMove)
				}
				data[k] = anchor
			}
		}
		e.write = model.BatchWrite{Method: http.MethodPost, Path: "/sections/" + url.PathEscape(section) + "/addTask", Data: data}
		e.refresh = true
	case outbox.KindAddTag, outbox.KindRemoveTag:
		tag, err := s.Store.RealTagGID(ctx, str("tag"))
		if err != nil {
			return e, false, err
		}
		if !real(tag) {
			return e, false, nil // a tag created offline is created in Asana first
		}
		action := "/addTag"
		if op.Kind == outbox.KindRemoveTag {
			action = "/removeTag"
		}
		e.write = model.BatchWrite{Method: http.MethodPost, Path: taskPath + action, Data: map[string]any{"tag": tag}}
	case outbox.KindAddFollower, outbox.KindRemoveFollower:
		user := str("user")
		if !real(user) {
			return e, false, nil
		}
		action := "/addFollowers"
		if op.Kind == outbox.KindRemoveFollower {
			action = "/removeFollowers"
		}
		e.write = model.BatchWrite{Method: http.MethodPost, Path: taskPath + action, Data: map[string]any{"followers": []string{user}}}
	case outbox.KindAddProject:
		project, section := str("project"), str("section")
		if !real(project) || (section != "" && !real(section)) {
			return e, false, nil
		}
		data := map[string]any{"project": project}
		if section != "" {
			data["section"] = section
		}
		e.write = model.BatchWrite{Method: http.MethodPost, Path: taskPath + "/addProject", Data: data}
	case outbox.KindRemoveProject:
		project := str("project")
		if !real(project) {
			return e, false, nil
		}
		e.write = model.BatchWrite{Method: http.MethodPost, Path: taskPath + "/removeProject", Data: map[string]any{"project": project}}
	case outbox.KindAddDependency, outbox.KindRemoveDependency:
		dep, err := s.Store.RealGID(ctx, str("dependency"))
		if err != nil {
			return e, false, err
		}
		if !real(dep) {
			return e, false, nil
		}
		action := "/addDependencies"
		if op.Kind == outbox.KindRemoveDependency {
			action = "/removeDependencies"
		}
		e.write = model.BatchWrite{Method: http.MethodPost, Path: taskPath + action, Data: map[string]any{"dependencies": []string{dep}}}
	default:
		return e, false, nil
	}
	return e, true, nil
}

// batchSent is what sending one entry of a batch gave (as send returns it).
type batchSent struct {
	out outcome
	err error
}

// pushBatch sends group (see batchGroup) and records what happened to each entry like
// pushEntry does. again lists the entries to handle once more right away (e.g. to check
// Asana after a 5xx). An error stops the push, after every entry has been recorded.
func (s *Syncer) pushBatch(ctx context.Context, group []batchEntry, res *PushResult, drops *dropState) (again map[int64]bool, err error) {
	var live []batchEntry
	for _, e := range group {
		if ok, err := s.Store.BeginSend(ctx, e.op.ID, s.clock()); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		// Read it again now that it is "sending": an edit made since the group was formed
		// may have been joined to it (or reverted it); one made from now on gets an entry of
		// its own. (No entry here is verified, so the sent_at BeginSend sets is not used.)
		op, err := s.Store.GetOp(ctx, e.op.ID)
		if errors.Is(err, readmodel.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		fresh, ok, err := s.batchEntryFor(ctx, *op)
		if err != nil {
			return nil, err
		}
		if !ok || fresh.task != e.task {
			fresh = batchEntry{op: *op, task: e.task, alone: true}
		}
		e = fresh
		if err := s.Store.CountAttempt(ctx, e.op.ID, s.clock()); err != nil {
			return nil, err
		}
		e.op.Attempts++
		live = append(live, e)
	}
	results := s.sendBatch(ctx, live)
	again = map[int64]bool{}
	var stop error
	for i, e := range live {
		a, err := s.handleOutcome(ctx, e.op, results[i].out, results[i].err, false, res, drops)
		if err != nil {
			if stop == nil {
				stop = err
			}
			continue
		}
		again[e.op.ID] = a
	}
	return again, stop
}

// sendBatch sends entries (already marked "sending") and returns what each gave.
func (s *Syncer) sendBatch(ctx context.Context, entries []batchEntry) []batchSent {
	results := make([]batchSent, len(entries))
	alone := make([]bool, len(entries)) // to send on its own instead
	for i, e := range entries {
		alone[i] = e.alone || len(entries) == 1
	}

	// Compare with the tasks in Asana first (one more batch, reading).
	var check []string
	for i, e := range entries {
		if e.check && !alone[i] {
			check = append(check, e.task)
		}
	}
	server := map[string]*model.Task{}
	if len(check) > 0 {
		tasks, _, err := s.API.TasksByGID(ctx, check)
		switch {
		case err == nil:
		case isTemporary(err) || model.IsUnauthorized(err):
			// Nothing was sent: each is handled as if its own read had failed so.
			for i := range results {
				results[i].err = err
			}
			return results
		default: // e.g. an answer that cannot be read: each is sent on its own, as before
			for i := range alone {
				alone[i] = true
			}
		}
		for i := range tasks {
			server[tasks[i].GID] = &tasks[i]
		}
	}
	var writes []model.BatchWrite
	var sentIdx []int // the entry of each write
	for i, e := range entries {
		if alone[i] {
			continue
		}
		if e.check {
			t := server[e.task]
			if t == nil { // not shown to us (gone, or no access): sent alone, it tells which
				alone[i] = true
				continue
			}
			var conflicts map[string]any
			var err error
			if e.op.Kind == outbox.KindSetCustomField {
				conflicts, err = customFieldConflict(e.op, t)
			} else {
				conflicts = updateConflicts(e.op, t)
			}
			if err != nil {
				results[i].err = err
				continue
			}
			if conflicts != nil {
				results[i] = batchSent{conflicted, s.Store.MarkOpConflict(ctx, e.op.ID, conflicts)}
				continue
			}
		}
		writes = append(writes, e.write)
		sentIdx = append(sentIdx, i)
	}

	var moved []string
	if len(writes) > 0 {
		answers, err := s.API.Batch(ctx, writes)
		switch {
		case err == nil:
		case isTemporary(err) || model.IsUnauthorized(err):
			// Asana may have applied any of them: each is handled as if its own request
			// had failed so.
			for _, i := range sentIdx {
				results[i].err = err
			}
			if isNetworkError(err) || isRateLimited(err) || model.IsUnauthorized(err) {
				// The push stops here: the entries still to be sent alone wait too (not
				// sent, as if their own request had failed so).
				for i := range alone {
					if alone[i] {
						results[i].err, alone[i] = err, false
					}
				}
			}
		default: // the batch was refused, or its answer could not be read: one by one
			for _, i := range sentIdx {
				alone[i] = true
			}
		}
		for k, a := range answers {
			i, e := sentIdx[k], entries[sentIdx[k]]
			var apiErr *model.Error
			switch {
			case a.Err == nil:
				if err := s.Store.CompleteOp(ctx, e.op.ID); err != nil {
					results[i].err = sentNotSaved(err)
					continue
				}
				if a.Task != nil {
					s.refresh(ctx, *a.Task)
				} else if e.refresh {
					moved = append(moved, e.task)
				}
			case errors.As(a.Err, &apiErr) && apiErr.Status == http.StatusBadRequest:
				alone[i] = true // sent alone, the answer says what it means (e.g. pushCustomField)
			default:
				results[i].err = a.Err
			}
		}
	}
	for i, e := range entries {
		if alone[i] {
			results[i].out, results[i].err = s.send(ctx, e.op)
		}
	}
	if len(moved) > 0 { // else the next sync refreshes them
		if tasks, _, err := s.API.TasksByGID(ctx, moved); err == nil {
			for _, t := range tasks {
				s.refresh(ctx, t)
			}
		}
	}
	return results
}
