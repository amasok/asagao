package sync

import (
	"context"
	"strings"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// Verifying entries whose result is unknown (see store/outbox_send.go).
//
// Asana has no idempotency keys (and a task's external.gid can only be set by OAuth apps,
// while asana-tui also supports personal access tokens), so reconcile looks for what the
// entry would have created: same name/text, created at or after the entry was first sent
// (minus clockSkew, as the local clock and Asana's may differ), by us where Asana says who
// created it. One candidate is taken as the entry's result; none means it did not reach
// Asana; several (or no way to look) are left to the user.

// match is what reconcile found in Asana.
type match int

const (
	notFound  match = iota // send the entry again
	matched                // recorded as sent
	ambiguous              // the user must check
)

const clockSkew = 2 * time.Minute

// reconcile checks Asana for the result of op (already marked "sending"). On a match it
// records the result like the matching push function would. An error means Asana could
// not be checked.
func (s *Syncer) reconcile(ctx context.Context, op store.Op) (outcome, match, error) {
	since := time.Time{}
	if t, err := time.Parse(time.RFC3339, op.SentAt); err == nil {
		since = t.Add(-clockSkew)
	}
	me, err := s.Store.SyncState(ctx, store.KeyMeGID) // "" before the first sync: not filtered
	if err != nil {
		return sent, notFound, err
	}
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	byMe := func(r *asana.Ref) bool { return r == nil || me == "" || r.GID == me }
	recent := func(t time.Time) bool { return !t.Before(since) }

	switch op.Kind {
	case store.OpCreateTask:
		tasks, ok, err := s.tasksCreatedUnder(ctx, str("parent"), str("project"), str("assignee"), str("workspace"), since)
		if err != nil || !ok {
			return sent, ambiguousUnless(ok), err
		}
		t, m := pick(tasks, func(t asana.Task) bool { return t.Name == str("name") && recent(t.CreatedAt) && byMe(t.CreatedBy) })
		if m != matched {
			return sent, m, nil
		}
		var move *store.MoveTarget
		if section := str("section"); section != "" {
			move = &store.MoveTarget{Project: str("project"), Section: section, From: sectionIn(&t, str("project"))}
		}
		if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, move); err != nil {
			return sent, notFound, err
		}
		s.refresh(ctx, t)
		if move != nil {
			return followUp, matched, nil
		}
		return sent, matched, nil

	case store.OpDuplicateTask:
		// The copy goes where the source is (same parent, or the source's first project).
		src, err := s.API.Task(ctx, str("source"))
		if err != nil {
			return sent, notFound, err
		}
		parent, project, assignee := "", "", ""
		if src.Parent != nil {
			parent = src.Parent.GID
		}
		if len(src.Memberships) > 0 && src.Memberships[0].Project != nil {
			project = src.Memberships[0].Project.GID
		}
		if src.Assignee != nil {
			assignee = src.Assignee.GID
		}
		ws := ""
		if src.Workspace != nil {
			ws = src.Workspace.GID
		}
		tasks, ok, err := s.tasksCreatedUnder(ctx, parent, project, assignee, ws, since)
		if err != nil || !ok {
			return sent, ambiguousUnless(ok), err
		}
		t, m := pick(tasks, func(t asana.Task) bool { return t.Name == str("name") && t.GID != src.GID && recent(t.CreatedAt) })
		if m != matched {
			return sent, m, nil
		}
		if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, nil); err != nil {
			return sent, notFound, err
		}
		s.refresh(ctx, t)
		return sent, matched, nil

	case store.OpAddComment:
		stories, err := s.API.Stories(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		st, m := pick(stories, func(st asana.Story) bool {
			return st.Type == "comment" && byMe(st.CreatedBy) && recent(st.CreatedAt) && sameText(st.Text, str("text"))
		})
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteComment(ctx, op.ID, str("story_gid"), op.TaskGID, st)

	case store.OpUploadAttachment:
		atts, err := s.API.Attachments(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		a, m := pick(atts, func(a asana.Attachment) bool { return a.Name == str("name") && recent(a.CreatedAt) })
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteUpload(ctx, op.ID, str("attachment"), a.GID, a.ViewURL)

	case store.OpCreateStatus:
		ups, err := s.API.StatusUpdates(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		u, m := pick(ups, func(u asana.StatusUpdate) bool {
			return u.Title == str("title") && u.StatusType == str("status_type") && recent(u.CreatedAt) && byMe(u.Author)
		})
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteStatus(ctx, op.ID, str("status"), u)

	case store.OpAddTag:
		// A tag created offline is created before it is added. If a tag with that name
		// exists, use it; the add itself is harmless to repeat, so it is sent either way.
		tag, err := s.Store.RealTagGID(ctx, str("tag"))
		if err != nil || !store.IsLocalGID(tag) {
			return sent, notFound, err
		}
		tags, err := s.API.Tags(ctx, str("workspace"))
		if err != nil {
			return sent, notFound, err
		}
		g, m := pick(tags, func(g asana.Ref) bool { return g.Name == str("tag_name") })
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
	return sent, notFound, nil // harmless to send again
}

// tasksCreatedUnder lists the tasks a new task could be among: the parent's subtasks, the
// project's tasks, or the assignee's tasks (modified since since). ok is false when there
// is no list to look in (no parent, project or assignee).
func (s *Syncer) tasksCreatedUnder(ctx context.Context, parent, project, assignee, workspace string, since time.Time) ([]asana.Task, bool, error) {
	var tasks []asana.Task
	var err error
	switch {
	case parent != "":
		if parent, err = s.Store.RealGID(ctx, parent); err != nil {
			return nil, false, err
		}
		tasks, err = s.API.Subtasks(ctx, parent)
	case project != "":
		tasks, err = s.API.Tasks(ctx, asana.TaskQuery{Project: project, ModifiedSince: since})
	case assignee != "" && workspace != "":
		tasks, err = s.API.Tasks(ctx, asana.TaskQuery{Assignee: assignee, Workspace: workspace, ModifiedSince: since})
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

// sameText compares comment text ignoring differences in spacing (Asana derives the plain
// text of a rich-text comment itself).
func sameText(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}
