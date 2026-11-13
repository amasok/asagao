package sync

import (
	"context"
	"html"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

// Verifying entries whose result is unknown (see store/outbox_send.go).
//
// Asana has no idempotency keys (and a task's external.gid can only be set by OAuth apps,
// while asana-tui also supports personal access tokens), so reconcile looks for what the
// entry would have created: same name/text (and, for tasks, the same due date, assignee
// and notes), created at or after the entry was first sent (minus clockSkew, as the local
// clock and Asana's may differ), by us where Asana says who created it, and not already
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

const clockSkew = 2 * time.Minute

// settleTime is how long after sending an entry its result may still be missing from
// Asana's lists. A duplicate is copied by a background job, so it takes longer.
func settleTime(kind string) time.Duration {
	if kind == store.OpDuplicateTask {
		return 5 * time.Minute
	}
	return time.Minute
}

// reconcile checks Asana for the result of op (already marked "sending"). On a match it
// records the result like the matching push function would. An error means Asana could
// not be checked.
func (s *Syncer) reconcile(ctx context.Context, op store.Op) (outcome, match, error) {
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
	out, m, err := s.find(ctx, op, sentAt.Add(-clockSkew))
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

func (s *Syncer) find(ctx context.Context, op store.Op, since time.Time) (outcome, match, error) {
	me, err := s.Store.SyncState(ctx, store.KeyMeGID) // "" before the first sync: not filtered
	if err != nil {
		return sent, notFound, err
	}
	str := func(k string) string { v, _ := op.Payload[k].(string); return v }
	byMe := func(r *asana.Ref) bool { return r == nil || me == "" || r.GID == me }
	recent := func(t time.Time) bool { return !t.Before(since) }
	taken := func(prefix string) (map[string]bool, error) { return s.Store.RecordedResults(ctx, prefix) }

	switch op.Kind {
	case store.OpCreateTask:
		tasks, ok, err := s.tasksCreatedUnder(ctx, str("parent"), str("project"), str("assignee"), str("workspace"), since)
		if err != nil || !ok {
			return sent, ambiguousUnless(ok), err
		}
		used, err := taken(store.ResultTask)
		if err != nil {
			return sent, notFound, err
		}
		t, m := pick(tasks, func(t asana.Task) bool {
			return !used[t.GID] && t.Name == str("name") && recent(t.CreatedAt) && byMe(t.CreatedBy) && sameTaskFields(t, op.Payload)
		})
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
		used, err := taken(store.ResultTask)
		if err != nil {
			return sent, notFound, err
		}
		t, m := pick(tasks, func(t asana.Task) bool {
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

	case store.OpAddComment:
		stories, err := s.API.Stories(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		used, err := taken(store.ResultStory)
		if err != nil {
			return sent, notFound, err
		}
		st, m := pick(stories, func(st asana.Story) bool {
			return !used[st.GID] && st.Type == "comment" && byMe(st.CreatedBy) && recent(st.CreatedAt) &&
				sameComment(st, str("text"), str("html_text"))
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
		used, err := taken(store.ResultAttachment)
		if err != nil {
			return sent, notFound, err
		}
		a, m := pick(atts, func(a asana.Attachment) bool { return !used[a.GID] && a.Name == str("name") && recent(a.CreatedAt) })
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteUpload(ctx, op.ID, str("attachment"), a.GID, a.ViewURL)

	case store.OpCreateStatus:
		ups, err := s.API.StatusUpdates(ctx, op.TaskGID)
		if err != nil {
			return sent, notFound, err
		}
		used, err := taken(store.ResultStatus)
		if err != nil {
			return sent, notFound, err
		}
		u, m := pick(ups, func(u asana.StatusUpdate) bool {
			return !used[u.GID] && u.Title == str("title") && u.StatusType == str("status_type") && recent(u.CreatedAt) && byMe(u.Author)
		})
		if m != matched {
			return sent, m, nil
		}
		return sent, matched, s.Store.CompleteStatus(ctx, op.ID, str("status"), u)

	case store.OpAddTag:
		// A tag created offline is created before it is added. If a tag with that name
		// exists (whoever made it), use it; the add itself is harmless to repeat, so it is
		// sent either way.
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
	return sent, notFound, nil
}

// sameTaskFields compares the fields a create sets besides the name, so a task someone
// made by hand with the same name is not taken for ours.
func sameTaskFields(t asana.Task, payload map[string]any) bool {
	str := func(k string) string { v, _ := payload[k].(string); return v }
	if str("due_on") != t.DueOn {
		return false
	}
	assignee := ""
	if t.Assignee != nil {
		assignee = t.Assignee.GID
	}
	if str("assignee") != assignee {
		return false
	}
	if _, rich := payload["html_notes"]; rich {
		return textKey(stripHTML(t.HTMLNotes)) == textKey(stripHTML(str("html_notes"))) ||
			textKey(t.Notes) == textKey(str("notes"))
	}
	return textKey(t.Notes) == textKey(str("notes"))
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

// sameComment reports whether a story is the comment we sent. Asana makes the plain text
// of a rich-text comment itself (links, mentions and lists come out differently from our
// Markdown-derived text), so the visible words of either form are compared.
func sameComment(st asana.Story, text, htmlText string) bool {
	if textKey(text) == "" { // only emoji or symbols: nothing to compare loosely
		return strings.TrimSpace(st.Text) == strings.TrimSpace(text)
	}
	if textKey(st.Text) == textKey(text) {
		return true
	}
	return htmlText != "" && st.HTMLText != "" && textKey(stripHTML(st.HTMLText)) == textKey(stripHTML(htmlText))
}

var (
	// mentionOrImage matches <a>/<img> elements Asana fills in itself from data-asana-gid
	// (we send them empty; Asana returns the user's name or the image).
	mentionOrImage = regexp.MustCompile(`(?s)<(a|img)\b[^>]*data-asana-gid[^>]*?(/>|>.*?</a>)`)
	tagRE          = regexp.MustCompile(`(?s)<[^>]*>`)
	urlRE          = regexp.MustCompile(`https?://\S+`)
)

// stripHTML returns the visible text of Asana rich text, without mentions and images.
func stripHTML(s string) string {
	s = mentionOrImage.ReplaceAllString(s, " ")
	return html.UnescapeString(tagRE.ReplaceAllString(s, " "))
}

// textKey keeps only letters and digits (URLs removed), so spacing, punctuation, list
// markers and link targets do not matter.
func textKey(s string) string {
	s = urlRE.ReplaceAllString(s, " ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
