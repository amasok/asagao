package sync

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
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
func settleTime(kind string) time.Duration {
	if kind == outbox.KindDuplicateTask {
		return 5 * time.Minute
	}
	return time.Minute
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

func (s *Syncer) find(ctx context.Context, op outbox.Op, since time.Time) (outcome, match, error) {
	me, err := s.Store.SyncState(ctx, readmodel.KeyMeGID) // "" before the first sync: not filtered
	if err != nil {
		return sent, notFound, err
	}
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	byMe := func(r *model.Ref) bool { return r == nil || me == "" || r.GID == me }
	recent := func(t time.Time) bool { return !t.Before(since) }
	taken := func(prefix string) (map[string]bool, error) { return s.Store.RecordedResults(ctx, prefix) }

	switch op.Kind {
	case outbox.KindCreateSection:
		return s.findCreatedSection(ctx, op)
	case outbox.KindCreateTask:
		tasks, ok, err := s.tasksCreatedUnder(ctx, str("parent"), str("project"), str("assignee"), str("workspace"), since)
		if err != nil || !ok {
			return sent, ambiguousUnless(ok), err
		}
		used, err := taken(readmodel.ResultTask)
		if err != nil {
			return sent, notFound, err
		}
		t, m := pick(tasks, func(t model.Task) bool {
			return !used[t.GID] && t.Name == str("name") && recent(t.CreatedAt) && byMe(t.CreatedBy) && sameTaskFields(t, op.Payload)
		})
		if m != matched {
			return sent, m, nil
		}
		var move *outbox.MoveTarget
		if section := str("section"); section != "" {
			move = &outbox.MoveTarget{Project: str("project"), Section: section, From: sectionIn(&t, str("project"))}
		}
		if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, move); err != nil {
			return sent, notFound, err
		}
		s.refresh(ctx, t)
		if move != nil {
			return followUp, matched, nil
		}
		return sent, matched, nil

	case outbox.KindDuplicateTask:
		// The copy goes where the source is (same parent, or the source's first project).
		src, err := s.API.Task(ctx, str("source"))
		if err != nil {
			return sent, notFound, err
		}
		parent, project, assignee, ws := "", "", "", ""
		if src.Parent != nil {
			parent = src.Parent.GID
		}
		if len(src.Memberships) > 0 && src.Memberships[0].Project != nil {
			project = src.Memberships[0].Project.GID
		}
		if src.Assignee != nil {
			assignee = src.Assignee.GID
		}
		if src.Workspace != nil {
			ws = src.Workspace.GID
		}
		tasks, ok, err := s.tasksCreatedUnder(ctx, parent, project, assignee, ws, since)
		if err != nil || !ok {
			return sent, ambiguousUnless(ok), err
		}
		used, err := taken(readmodel.ResultTask)
		if err != nil {
			return sent, notFound, err
		}
		t, m := pick(tasks, func(t model.Task) bool {
			return !used[t.GID] && t.Name == str("name") && t.GID != src.GID && recent(t.CreatedAt)
		})
		if m != matched {
			return sent, m, nil
		}
		if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, nil); err != nil {
			return sent, notFound, err
		}
		s.refresh(ctx, t)
		return sent, matched, nil

	case outbox.KindAddComment:
		stories, err := s.API.Stories(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		used, err := taken(readmodel.ResultStory)
		if err != nil {
			return sent, notFound, err
		}
		st, m := pick(stories, func(st model.Story) bool {
			return !used[st.GID] && st.Type == "comment" && byMe(st.CreatedBy) && recent(st.CreatedAt) &&
				sameComment(st, str("text"), str("html_text"))
		})
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteComment(ctx, op.ID, str("story_gid"), op.TaskGID, st)

	case outbox.KindAddTimeEntry:
		return s.findTimeEntry(ctx, op, byMe, recent)

	case outbox.KindUploadAttachment:
		atts, err := s.API.Attachments(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		used, err := taken(readmodel.ResultAttachment)
		if err != nil {
			return sent, notFound, err
		}
		a, m := pick(atts, func(a model.Attachment) bool { return !used[a.GID] && a.Name == str("name") && recent(a.CreatedAt) })
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteUpload(ctx, op.ID, str("attachment"), a.GID, a.ViewURL)

	case outbox.KindCreateStatus:
		ups, err := s.API.StatusUpdates(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		used, err := taken(readmodel.ResultStatus)
		if err != nil {
			return sent, notFound, err
		}
		u, m := pick(ups, func(u model.StatusUpdate) bool {
			return !used[u.GID] && u.Title == str("title") && u.StatusType == str("status_type") && recent(u.CreatedAt) && byMe(u.Author)
		})
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteStatus(ctx, op.ID, str("status"), u)

	case outbox.KindAddTag:
		// A tag created offline is created before it is added. If a tag with that name
		// exists (whoever made it), use it; the add itself is harmless to repeat, so it is
		// sent either way.
		tag, err := s.Store.RealTagGID(ctx, str("tag"))
		if err != nil || !localgid.IsLocal(tag) {
			return sent, notFound, err
		}
		tags, err := s.API.Tags(ctx, str("workspace"))
		if err != nil {
			return sent, notFound, err
		}
		g, m := pick(tags, func(g model.Tag) bool { return g.Name == str("tag_name") })
		switch m {
		case ambiguous:
			return sent, ambiguous, nil
		case matched:
			if err := s.Store.ReplaceLocalTagGID(ctx, tag, g.GID); err != nil {
				return sent, notFound, err
			}
		}
		return sent, notFound, nil
	}
	return sent, notFound, nil
}

// sameTaskFields is outbox.SameTaskFields on the task as Asana returns it.
func sameTaskFields(t model.Task, payload map[string]any) bool {
	f := outbox.TaskFields{DueOn: t.DueOn, Notes: t.Notes, HTMLNotes: t.HTMLNotes}
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
