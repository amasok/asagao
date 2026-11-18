package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/dates"
	"github.com/amasok/asagao/internal/domain/timetrack"
	"github.com/amasok/asagao/internal/readmodel"
)

// TimeTrackingStore is the part of the local cache that the time tracking use case needs.
// *store.Store satisfies it.
type TimeTrackingStore interface {
	Me(ctx context.Context) (readmodel.UserRef, error)
	SyncState(ctx context.Context, key string) (string, error)
	TimeEntries(ctx context.Context, taskGID string) (readmodel.TimeEntries, error)
	TimeEntryTask(ctx context.Context, gid string) (string, error)
	AddTimeEntryLocal(ctx context.Context, taskGID string, minutes int, enteredOn string, me readmodel.UserRef, now time.Time) (string, error)
	UpdateTimeEntryLocal(ctx context.Context, gid string, minutes int, enteredOn string, now time.Time) error
	DeleteTimeEntryLocal(ctx context.Context, gid string, now time.Time) error
}

// TimeTracking is the use case of the time tracking entries of tasks (#6): the time worked
// on a task, recorded per day. Asana offers it on paid plans only; once it refused it (402)
// Available is false and the writes return an *ErrPlanUnavailable.
type TimeTracking struct {
	Store TimeTrackingStore
	Clock Clock
}

// Available reports whether time tracking can be offered.
func (t *TimeTracking) Available(ctx context.Context) bool {
	return planAvailable(ctx, t.Store, readmodel.PlanTimeTracking) == nil
}

// List returns a task's entries in the cache (fetching them is the sync engine's).
func (t *TimeTracking) List(ctx context.Context, taskGID string) (readmodel.TimeEntries, error) {
	return t.Store.TimeEntries(ctx, taskGID)
}

// ParseDuration reads a duration as the entry points accept it ("90", "1h30m", "1.5h",
// "1:30") in minutes.
func ParseDuration(s string) (int, error) { return timetrack.ParseDuration(s) }

// FormatDuration writes minutes as "1h30m".
func FormatDuration(minutes int) string { return timetrack.Format(minutes) }

func (t *TimeTracking) day(s string) (string, error) {
	if s == "" {
		s = "today"
	}
	d, err := dates.Parse(s, t.Clock())
	if err != nil {
		return "", &FieldError{Field: "date", Err: err}
	}
	return d, nil
}

// Add records the time duration (as ParseDuration reads it) on taskGID for day (as the due
// dates are written; "" is today), as me, locally and queued in the outbox. It returns the
// entry's local gid.
func (t *TimeTracking) Add(ctx context.Context, taskGID, duration, day string) (string, error) {
	if err := planAvailable(ctx, t.Store, readmodel.PlanTimeTracking); err != nil {
		return "", err
	}
	minutes, err := timetrack.ParseDuration(duration)
	if err != nil {
		return "", &FieldError{Field: "duration", Err: err}
	}
	on, err := t.day(day)
	if err != nil {
		return "", err
	}
	me, err := t.Store.Me(ctx)
	if err != nil {
		return "", err
	}
	return t.Store.AddTimeEntryLocal(ctx, taskGID, minutes, on, me, t.Clock())
}

// Edit changes an entry's duration and/or day ("" keeps it), locally and queued.
func (t *TimeTracking) Edit(ctx context.Context, entryGID, duration, day string) error {
	if err := planAvailable(ctx, t.Store, readmodel.PlanTimeTracking); err != nil {
		return err
	}
	cur, err := t.entry(ctx, entryGID)
	if err != nil {
		return err
	}
	minutes, on := cur.DurationMinutes, cur.EnteredOn
	if duration != "" {
		if minutes, err = timetrack.ParseDuration(duration); err != nil {
			return &FieldError{Field: "duration", Err: err}
		}
	}
	if day != "" {
		if on, err = t.day(day); err != nil {
			return err
		}
	}
	return t.Store.UpdateTimeEntryLocal(ctx, entryGID, minutes, on, t.Clock())
}

// Delete deletes an entry, locally and queued (an entry not sent yet is just dropped).
func (t *TimeTracking) Delete(ctx context.Context, entryGID string) error {
	if err := planAvailable(ctx, t.Store, readmodel.PlanTimeTracking); err != nil {
		return err
	}
	return t.Store.DeleteTimeEntryLocal(ctx, entryGID, t.Clock())
}

// Entry returns one entry in the cache (readmodel.ErrNotFound if it is not there).
func (t *TimeTracking) Entry(ctx context.Context, entryGID string) (readmodel.TimeEntryView, error) {
	return t.entry(ctx, entryGID)
}

func (t *TimeTracking) entry(ctx context.Context, gid string) (readmodel.TimeEntryView, error) {
	task, err := t.Store.TimeEntryTask(ctx, gid)
	if err != nil {
		return readmodel.TimeEntryView{}, err
	}
	es, err := t.Store.TimeEntries(ctx, task)
	if err != nil {
		return readmodel.TimeEntryView{}, err
	}
	for _, e := range es.Entries {
		if e.GID == gid {
			return e, nil
		}
	}
	return readmodel.TimeEntryView{}, readmodel.ErrNotFound
}
