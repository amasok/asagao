package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/dates"
	"github.com/amasok/asagao/internal/domain/task"
)

// ParseDue reads a due date as a user types it: YYYY-MM-DD, today, tomorrow, yesterday,
// +Nd or -Nd (relative to now), or "" for no date. It returns YYYY-MM-DD or "". Every
// entry point reads due dates with it, directly or through a use case.
func ParseDue(s string, now time.Time) (string, error) { return dates.Parse(s, now) }

// UpdateTaskInput is what a user gives to change a task's fields. A nil field is left as
// it is. Due and Start (the start date: it needs a due date, not before it) are read by
// ParseDue ("" clears it), Assignee is "me", a user's gid or name
// ("" unassigns), Subtype and Approval are read by task.ParseSubtype and
// task.ParseApprovalStatus, and Notes is Markdown that replaces the description.
//
// Subtype may also be the name (or gid) of a custom task type the task's projects offer
// (#59), and CustomType is one; CustomTypeStatus is a status of the task's custom type (the
// new one, if it changes too), by name or gid. They need the task (ResolveUpdateFor).
type UpdateTaskInput struct {
	Name, Notes, Due, Start, Assignee, Subtype, Approval *string
	CustomType, CustomTypeStatus                         *string
	Completed, Liked                                     *bool
}

// TaskUpdate is a change to a task's fields with every value already resolved: Due and
// Start are YYYY-MM-DD or "", Assignee a user's gid or "", Subtype and Approval are Asana's values
// (resource_subtype, approval_status). Notes is still Markdown; it is converted when the
// update is applied. A nil field is left as it is.
//
// CustomType and CustomTypeStatus are gids (custom_type, custom_type_status_option); a
// CustomType comes with Subtype "custom". CustomTypeName and CustomTypeStatusName are
// their names, for saying what was set (not sent).
type TaskUpdate struct {
	Name, Notes, Due, Start, Assignee, Subtype, Approval *string
	CustomType, CustomTypeStatus                         *string
	Completed, Liked                                     *bool

	CustomTypeName, CustomTypeStatusName string
}

// Count is the number of fields the update changes.
func (u TaskUpdate) Count() int {
	n := 0
	for _, s := range []*string{u.Name, u.Notes, u.Due, u.Start, u.Assignee, u.Subtype, u.Approval, u.CustomType, u.CustomTypeStatus} {
		if s != nil {
			n++
		}
	}
	for _, b := range []*bool{u.Completed, u.Liked} {
		if b != nil {
			n++
		}
	}
	return n
}

// ResolveUpdate reads and checks the input without writing anything. An invalid value is
// a *FieldError; the fields are checked in the order resource_subtype, approval_status,
// due, start_on, assignee, and the first invalid one is returned. Only Assignee reads the store.
//
// A custom task type (#59) needs the task: ResolveUpdate refuses CustomType and
// CustomTypeStatus, and takes Subtype as one of task.Subtypes only.
func (t *Tasks) ResolveUpdate(ctx context.Context, in UpdateTaskInput) (TaskUpdate, error) {
	return t.resolveUpdate(ctx, "", in)
}

// ResolveUpdateFor is ResolveUpdate for the task gid: Subtype may also name a custom task
// type, and CustomType and CustomTypeStatus are read against the types the task can be
// given (the store's TaskKind). They are checked after resource_subtype, in the order
// custom_type, custom_type_status_option. The task is read only for them.
func (t *Tasks) ResolveUpdateFor(ctx context.Context, gid string, in UpdateTaskInput) (TaskUpdate, error) {
	return t.resolveUpdate(ctx, gid, in)
}

func (t *Tasks) resolveUpdate(ctx context.Context, gid string, in UpdateTaskInput) (TaskUpdate, error) {
	u := TaskUpdate{Name: in.Name, Notes: in.Notes, Completed: in.Completed, Liked: in.Liked}
	if err := t.resolveKind(ctx, gid, in, &u); err != nil {
		return TaskUpdate{}, err
	}
	if in.Approval != nil {
		v, err := task.ParseApprovalStatus(*in.Approval)
		if err != nil {
			return TaskUpdate{}, &FieldError{Field: "approval_status", Err: err}
		}
		u.Approval = &v
	}
	if in.Due != nil {
		v, err := ParseDue(*in.Due, t.Clock())
		if err != nil {
			return TaskUpdate{}, &FieldError{Field: "due", Err: err}
		}
		u.Due = &v
	}
	if in.Start != nil {
		v, err := ParseDue(*in.Start, t.Clock())
		if err != nil {
			return TaskUpdate{}, &FieldError{Field: "start_on", Err: err}
		}
		u.Start = &v
	}
	if in.Assignee != nil {
		v, err := t.resolveUser(ctx, *in.Assignee)
		if err != nil {
			return TaskUpdate{}, &FieldError{Field: "assignee", Err: err}
		}
		u.Assignee = &v
	}
	return u, nil
}

// UpdateHere applies resolved changes to the task gid, locally and queued in the outbox,
// in one transaction (the store's UpdateTaskLocal). An update that changes nothing writes
// nothing and is not an error. The errors are the store's, as they are (an empty name, an
// approval status on a task that is not an approval, a start date without a due date or
// after it, a start date where the plan lacks it (*ErrPlanUnavailable), a task not in the
// cache).
func (t *Tasks) UpdateHere(ctx context.Context, gid string, u TaskUpdate) error {
	changes := map[string]any{}
	for key, v := range map[string]*string{"name": u.Name, "due_on": u.Due, "start_on": u.Start, "assignee": u.Assignee,
		"resource_subtype": u.Subtype, "approval_status": u.Approval,
		"custom_type": u.CustomType, "custom_type_status_option": u.CustomTypeStatus} {
		if v != nil {
			changes[key] = *v
		}
	}
	for key, v := range map[string]*bool{"completed": u.Completed, "liked": u.Liked} {
		if v != nil {
			changes[key] = *v
		}
	}
	if u.Notes != nil {
		changes["html_notes"], changes["notes"] = t.Text.TaskNotes(*u.Notes)
	}
	if len(changes) == 0 {
		return nil
	}
	return t.Store.UpdateTaskLocal(ctx, gid, changes, t.Clock())
}

// Update resolves the input and applies it (ResolveUpdateFor, then UpdateHere). Nothing is
// written when the input is invalid. It returns the resolved update, so that the entry
// point can say what was set. The clock is read once, so that a relative due date and the
// time of the write are on the same day.
func (t *Tasks) Update(ctx context.Context, gid string, in UpdateTaskInput) (TaskUpdate, error) {
	now := t.Clock()
	once := *t
	once.Clock = func() time.Time { return now }
	u, err := once.ResolveUpdateFor(ctx, gid, in)
	if err != nil {
		return TaskUpdate{}, err
	}
	return u, once.UpdateHere(ctx, gid, u)
}
