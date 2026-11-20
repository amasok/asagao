package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
)

// Time tracking entries of tasks (#6). They are read when asked for (FetchTimeEntries),
// not by Run: most workspaces' plans lack them, and an entry does not change its task's
// modified_at. A 402 (from the list or from a write) records that the plan lacks the
// feature, and it is not offered (nor asked for) from then on.

func init() {
	entry := func(p outbox.TimeEntry) string { return p.Entry }
	registerOf(outbox.KindAddTimeEntry, handlerOf[outbox.TimeEntry]{send: sends((*Pusher).pushAddTimeEntry),
		find: func(s *Pusher, ctx context.Context, op outbox.Op, p outbox.TimeEntry, f finder) (outcome, match, error) {
			return s.findTimeEntry(ctx, op, p, f.byMe, f.recent)
		}})
	registerOf(outbox.KindUpdateTimeEntry, handlerOf[outbox.TimeEntry]{send: sends((*Pusher).pushUpdateTimeEntry), local: entry, localResult: readmodel.ResultTimeEntry})
	registerOf(outbox.KindDeleteTimeEntry, handlerOf[outbox.TimeEntry]{send: sends((*Pusher).pushDeleteTimeEntry), local: entry, localResult: readmodel.ResultTimeEntry})
}

// FetchTimeEntries refreshes a task's time tracking entries. It returns a
// *readmodel.PlanUnavailableError, without asking Asana, while a 402 in the task's
// workspace still hides time tracking (plan.go); after that it asks again, and a list read
// forgets the 402.
func (s *Details) FetchTimeEntries(ctx context.Context, taskGID string) error {
	if s.API == nil {
		return ErrOffline
	}
	ws, err := s.Store.TaskWorkspace(ctx, taskGID)
	if err != nil {
		return err
	}
	unavailable := &readmodel.PlanUnavailableError{Feature: plan.TimeTracking}
	if hidden, err := planHidden(ctx, s.Store, s.clock(), plan.TimeTracking, ws); err != nil || hidden {
		if err != nil {
			return err
		}
		return unavailable
	}
	if localgid.IsLocal(taskGID) {
		return nil // created offline; nothing on the server yet
	}
	// Entries Asana creates after this (an add sent meanwhile) are newer than the list.
	from := s.serverNow().Add(-clockSkew)
	entries, err := s.API.TimeEntries(ctx, taskGID)
	if refused, recErr := recordPlanRefusal(ctx, s.Store, s.clock(), plan.TimeTracking, ws, err); refused || recErr != nil {
		if recErr != nil {
			return recErr
		}
		return unavailable
	}
	if err != nil {
		return err
	}
	if err := s.Store.ClearPlanRefusal(ctx, plan.TimeTracking, ws); err != nil {
		return err
	}
	return s.Store.ReplaceTimeEntries(ctx, taskGID, entries, from)
}

// pushAddTimeEntry creates the entry. Sending it twice would record the time twice, so an
// answer that is lost is checked (findTimeEntry) before sending it again.
func (s *Pusher) pushAddTimeEntry(ctx context.Context, op outbox.Op, p outbox.TimeEntry) error {
	if localgid.IsLocal(op.TaskGID) {
		return &model.Error{Status: 404} // its task's create failed
	}
	e, err := s.API.CreateTimeEntry(ctx, op.TaskGID, p.DurationMinutes, p.EnteredOn)
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteTimeEntry(ctx, op.ID, p.Entry, op.TaskGID, *e))
}

// pushUpdateTimeEntry changes an entry; sending it twice is harmless (PUT). An entry
// deleted in Asana answers 404: the edit is dropped (dropGone).
func (s *Pusher) pushUpdateTimeEntry(ctx context.Context, op outbox.Op, p outbox.TimeEntry) error {
	real, _, err := s.Store.LocalResult(ctx, readmodel.ResultTimeEntry, p.Entry)
	if err != nil {
		return err
	}
	if real == "" {
		return &model.Error{Status: 404} // its add was dropped
	}
	e, err := s.API.UpdateTimeEntry(ctx, real, p.DurationMinutes, p.EnteredOn)
	if err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	if e.GID == "" {
		e.GID = real
	}
	_ = s.Store.ApplyTimeEntry(ctx, *e) // the next fetch fixes a failure
	return nil
}

// pushDeleteTimeEntry deletes an entry. A 404 means it is gone already, which is what was
// asked.
func (s *Pusher) pushDeleteTimeEntry(ctx context.Context, op outbox.Op, p outbox.TimeEntry) error {
	real, _, err := s.Store.LocalResult(ctx, readmodel.ResultTimeEntry, p.Entry)
	if err != nil {
		return err
	}
	if real != "" { // "": its add was dropped, so there is nothing in Asana to delete
		if err := s.API.DeleteTimeEntry(ctx, real); err != nil && !isNotFound(err) {
			return err
		}
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

// findTimeEntry looks in Asana for the entry an add_time_entry may have created: same
// minutes and day, by me, created since the add was sent, and not the result of another
// entry. A candidate the list gives without created_at (as the reference documents it) is
// read on its own.
func (s *Pusher) findTimeEntry(ctx context.Context, op outbox.Op, p outbox.TimeEntry, byMe func(*model.Ref) bool, recent func(time.Time) bool) (outcome, match, error) {
	if localgid.IsLocal(op.TaskGID) {
		return sent, notFound, nil // its task is not in Asana: nothing can have been created
	}
	minutes, enteredOn := p.DurationMinutes, p.EnteredOn
	entries, err := s.API.TimeEntries(ctx, op.TaskGID)
	if err != nil {
		return sent, notFound, err
	}
	used, err := s.Store.RecordedResults(ctx, readmodel.ResultTimeEntry)
	if err != nil {
		return sent, notFound, err
	}
	var found []model.TimeEntry
	for _, e := range entries {
		if used[e.GID] || e.DurationMinutes != minutes || (enteredOn != "" && e.EnteredOn != enteredOn) || !byMe(e.CreatedBy) {
			continue
		}
		full := &e
		if e.CreatedAt.IsZero() {
			full, err = s.API.TimeEntry(ctx, e.GID)
			if isNotFound(err) {
				continue // deleted meanwhile
			}
			if err != nil {
				return sent, notFound, err
			}
		}
		if full.CreatedAt.IsZero() {
			return sent, ambiguous, nil // no way to tell an older entry from ours
		}
		if recent(full.CreatedAt) {
			found = append(found, *full)
		}
	}
	e, m := pick(found, func(model.TimeEntry) bool { return true })
	if m != matched {
		return sent, m, nil
	}
	return sent, matched, s.Store.CompleteTimeEntry(ctx, op.ID, p.Entry, op.TaskGID, e)
}
