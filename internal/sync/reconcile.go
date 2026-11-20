package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/termsafe"
)

// Verifying entries whose result is unknown (see store/outbox_send.go).
//
// Asana has no idempotency keys (and a task's external.gid can only be set by OAuth apps,
// while asagao also supports personal access tokens), so reconcile looks for what the
// entry would have created: same name/text (and, for tasks, the same due date, assignee
// and notes), created at or after the entry was first sent (minus clockSkew), by us where Asana says who created it, and not already
// recorded as the result of another entry. One candidate is taken as the entry's result;
// several (or no way to look) are left to the user. None is trusted only once settleTime
// has passed since the entry was sent: right after a 5xx or a timeout, Asana may still be
// creating it (a duplicate is copied in the background), so until then it waits.

// match is what reconcile found in Asana.
type match int

const (
	notFound  match = iota // not in Asana: send the entry again
	matched                // recorded as sent
	ambiguous              // the user must check
	tooEarly               // not found yet, but Asana may still show it: check again later
)

// clockSkew is the margin when comparing when an entry was sent with Asana's created_at.
// Send times are recorded by this PC's clock; when compared, they are moved to Asana's by
// the offset measured from the Date header of Asana's latest response (by then known:
// the failed request's own answer, or the request that looks for the result), so a PC
// clock that is minutes or hours off does not matter. The margin covers what that
// measurement cannot (resolution, network delay).
const clockSkew = 2 * time.Minute

// settleTime is how long after sending an entry its result may still be missing from
// Asana's lists. A duplicate is copied by a background job, so it takes longer.
func settleTime(kind outbox.Kind) time.Duration {
	if d := handlers[kind].settle; d > 0 {
		return d
	}
	return settleDefault
}

// reconcile checks Asana for the result of op (already marked "sending"). On a match it
// records the result like the matching push function would. An error means Asana could
// not be checked.
func (s *Syncer) reconcile(ctx context.Context, op outbox.Op) (outcome, match, error) {
	safe, err := s.Store.SafeToResend(ctx, op)
	if err != nil || safe {
		return sent, notFound, err // harmless to send again: no need to look
	}
	sentAt, err := time.Parse(time.RFC3339, op.SentAt)
	if err != nil {
		return sent, ambiguous, nil // no idea since when to look
	}
	// Look for results since the first send, but trust "not there" only some time after
	// the latest request: Asana may still be processing it.
	lastSent := sentAt
	if t, err := time.Parse(time.RFC3339, op.LastSentAt); err == nil && t.After(lastSent) {
		lastSent = t
	}
	// sentAt is by this PC's clock, created_at by Asana's: compare them by Asana's. If no
	// answer has told the offset yet (a new process whose first request this is), ask.
	if !s.API.ClockKnown() {
		if _, err := s.API.Me(ctx); err != nil {
			return sent, notFound, err
		}
	}
	out, m, err := s.find(ctx, op, sentAt.Add(s.API.ClockOffset()-clockSkew))
	if err != nil || m != notFound || s.clock().Sub(lastSent) >= settleTime(op.Kind) {
		return out, m, err
	}
	// Not found so soon after sending: wait, unless what was found made it harmless to
	// send (a tag that exists now only needs adding).
	if safe, err = s.Store.SafeToResend(ctx, op); err != nil || safe {
		return out, m, err
	}
	return out, tooEarly, nil
}

// find looks in Asana for the result of op (see reconcile) with the find function of
// its kind; an entry without one has nothing to look for.
func (s *Syncer) find(ctx context.Context, op outbox.Op, since time.Time) (outcome, match, error) {
	h := handlers[op.Kind]
	if h.find == nil {
		return sent, notFound, nil
	}
	me, err := s.Store.SyncedMeGID(ctx) // "" before the first sync: not filtered
	if err != nil {
		return sent, notFound, err
	}
	return h.find(s, ctx, op, finder{since: since, me: me})
}

// sameTaskFields is outbox.SameTaskFields on the task as Asana returns it.
func sameTaskFields(t model.Task, payload map[string]any) bool {
	f := outbox.TaskFields{DueOn: t.DueOn, StartOn: t.StartOn, Notes: t.Notes, HTMLNotes: t.HTMLNotes}
	if t.Assignee != nil {
		f.AssigneeGID = t.Assignee.GID
	}
	return outbox.SameTaskFields(f, payload)
}

// tasksCreatedUnder lists the tasks a new task could be among: the parent's subtasks, the
// project's tasks, or the assignee's tasks (modified since since). ok is false when there
// is no list to look in (no parent, project or assignee).
func (s *Syncer) tasksCreatedUnder(ctx context.Context, parent, project, assignee, workspace string, since time.Time) ([]model.Task, bool, error) {
	var tasks []model.Task
	var err error
	switch {
	case parent != "":
		if parent, err = s.Store.RealGID(ctx, parent); err != nil {
			return nil, false, err
		}
		tasks, err = s.API.Subtasks(ctx, parent)
	case project != "":
		tasks, err = s.API.Tasks(ctx, model.TaskQuery{Project: project, ModifiedSince: since})
	case assignee != "" && workspace != "":
		tasks, err = s.API.Tasks(ctx, model.TaskQuery{Assignee: assignee, Workspace: workspace, ModifiedSince: since})
	default:
		return nil, false, nil
	}
	return tasks, err == nil, err
}

func ambiguousUnless(ok bool) match {
	if ok {
		return notFound
	}
	return ambiguous
}

// pick returns the one item matching keep: matched for exactly one, else notFound/ambiguous.
func pick[T any](items []T, keep func(T) bool) (T, match) {
	var found []T
	for _, it := range items {
		if keep(it) {
			found = append(found, it)
		}
	}
	var zero T
	switch len(found) {
	case 0:
		return zero, notFound
	case 1:
		return found[0], matched
	}
	return zero, ambiguous
}

// sameComment is outbox.SameComment on a story as Asana returns it.
func sameComment(st model.Story, text, htmlText string) bool {
	return outbox.SameComment(st.Text, st.HTMLText, text, htmlText, termsafe.String)
}
