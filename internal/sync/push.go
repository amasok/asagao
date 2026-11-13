package sync

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"slices"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/store"
)

type PushResult struct {
	Sent      int `json:"sent"`
	Conflicts int `json:"conflicts"`
	Failed    int `json:"failed"`
	// Remaining is the number of pending entries left (e.g. because we went offline).
	Remaining int `json:"remaining"`
	// Busy is set when another process was already sending (Push returns ErrPushBusy).
	Busy bool `json:"busy,omitempty"`
	// Discarded lists changes dropped because what they change was deleted in Asana. They
	// are also kept for the front ends to show once (store.PendingDiscards).
	Discarded []store.Discard `json:"discarded,omitempty"`
}

// ErrPushBusy is returned by Push when another process (TUI, CLI, MCP server) is sending
// the outbox. The entries stay queued; they are sent by that process or the next push.
var ErrPushBusy = store.ErrPushBusy

// outcome is what happened to an entry Asana accepted.
type outcome int

const (
	sent       outcome = iota
	conflicted         // left for the user to resolve
	followUp           // sent; the entry now holds a follow-up change to send right away
)

// sentNotSavedError wraps a failure to record a change that Asana already accepted.
type sentNotSavedError struct{ err error }

func (e *sentNotSavedError) Error() string {
	return "Asana には送信済みですが、ローカルへの反映に失敗しました（次の同期で確かめます）: " + e.err.Error()
}
func (e *sentNotSavedError) Unwrap() error { return e.err }

func sentNotSaved(err error) error {
	if err == nil {
		return nil
	}
	return &sentNotSavedError{err}
}

// Push sends pending outbox entries to Asana in order.
//
// Only one process pushes at a time (a lock file next to the database); if another one is
// pushing, Push returns ErrPushBusy at once.
//
// An update is checked against the server first: if a field we change was also changed
// on the server (server value differs from both our base and our new value), the entry is
// marked as a conflict and left for the user to resolve.
//
// When Asana may or may not have applied an entry (a 5xx, a dropped connection, a process
// that died while sending), an entry that is harmless to repeat is sent again; any other
// (a create) is verified: Push looks in Asana for what it created and records that, or
// sends it again if it is not there (see reconcile.go). The user is asked only if Asana
// cannot tell.
//
// Network errors (and 429 after the client's retries) stop Push and are returned so the
// caller can treat it as offline; the entries stay queued. Other errors (e.g. the task was
// deleted) mark the entry failed and Push continues. Changes to a task whose create has
// not been sent yet wait for it.
func (s *Syncer) Push(ctx context.Context) (res PushResult, err error) {
	unlock, err := s.Store.TryLockPush()
	if err != nil {
		if errors.Is(err, store.ErrPushBusy) {
			res.Busy = true
			res.Remaining, _, _, _ = s.Store.OutboxCounts(ctx)
		}
		return res, err
	}
	defer unlock()
	if _, _, err := s.Store.RecoverInterruptedOps(ctx); err != nil {
		return res, err
	}
	ops, err := s.Store.PendingOps(ctx)
	if err != nil {
		return res, err
	}
	defer func() { // report what is still queued (offline, waiting, or to be verified)
		res.Remaining, _, _, _ = s.Store.OutboxCounts(context.WithoutCancel(ctx))
	}()
	verified := map[int64]bool{} // checked in Asana during this push (at most once each)
	for _, snapshot := range ops {
		// An entry is handled a bounded number of times per push, whatever happens: send,
		// check + send again, the follow-up move of a create, and that move's retry. What is
		// left over waits for the next push.
		for step, again := 0, true; again && step < maxStepsPerEntry; step++ {
			again = false
			if err := ctx.Err(); err != nil {
				return res, err
			}
			// Re-read: sending an earlier entry may have rewritten this one (a create replaces
			// the local gid that later comments/updates refer to).
			op, err := s.Store.GetOp(ctx, snapshot.ID)
			if errors.Is(err, store.ErrNotFound) {
				break
			}
			if err != nil {
				return res, err
			}
			if op.State != store.OpPending || (op.Verify && verified[op.ID]) {
				break // sent again during this push already: verify on the next one
			}
			if wait, err := s.waitsForCreate(ctx, *op); err != nil {
				return res, err
			} else if wait {
				break
			}
			if ok, err := s.Store.BeginSend(ctx, op.ID, s.clock()); err != nil {
				return res, err
			} else if !ok {
				break
			}
			wasFollowUp := op.Kind != snapshot.Kind
			var out outcome
			sendIt := true
			if op.Verify {
				verified[op.ID] = true
				var m match
				out, m, err = s.reconcile(ctx, *op)
				switch {
				case isNotFound(err): // what it changes was deleted in Asana
					if err := s.dropGone(ctx, *op, err, &res); err != nil {
						return res, err
					}
					sendIt = false
				case isRejected(err): // e.g. not ours (403)
					if err := s.Store.MarkOpFailed(ctx, op.ID, fmt.Errorf("Asana に届いたか確かめる先にアクセスできません: %w", err)); err != nil {
						return res, err
					}
					res.Failed++
					sendIt = false
				case err != nil: // could not check: try again on the next push
					note := "Asana に届いたか確かめられませんでした（次の同期で確かめます）: " + err.Error()
					if rerr := s.Store.MarkOpVerify(context.WithoutCancel(ctx), op.ID, note); rerr != nil {
						return res, rerr
					}
					if isNetworkError(err) || isRateLimited(err) {
						return res, err
					}
					sendIt = false
				case m == tooEarly: // Asana may not list it yet: check again on the next push
					if err := s.Store.MarkOpVerify(ctx, op.ID, "Asana への反映を待っています（次の同期で確かめます）"); err != nil {
						return res, err
					}
					sendIt = false
				case m == ambiguous:
					if err := s.Store.MarkOpFailed(ctx, op.ID, errors.New(store.AmbiguousMessage)); err != nil {
						return res, err
					}
					res.Failed++
					sendIt = false
				case m == matched: // Asana has it: recorded without sending it again
					if !wasFollowUp {
						res.Sent++
					}
					again = out == followUp
					sendIt = false
				default: // not in Asana: send it again
					if err := s.Store.ClearVerify(ctx, op.ID); err != nil {
						return res, err
					}
				}
			}
			if !sendIt {
				continue
			}
			if err := s.Store.CountAttempt(ctx, op.ID, s.clock()); err != nil {
				return res, err
			}
			op.Attempts++
			out, err = s.send(ctx, *op)
			switch {
			case err == nil:
				switch {
				case out == conflicted:
					res.Conflicts++
				case !wasFollowUp:
					res.Sent++ // a follow-up is part of the change already counted
				}
				again = out == followUp
			case isNetworkError(err) || isRateLimited(err):
				if err := s.holdBack(ctx, *op, err); err != nil {
					return res, err
				}
				return res, err // offline or cancelled: keep the rest for later
			case isNotFound(err): // what it changes was deleted in Asana
				if err := s.dropGone(ctx, *op, err, &res); err != nil {
					return res, err
				}
			default:
				verify, ferr := s.handleError(ctx, *op, err)
				if ferr != nil {
					return res, ferr
				}
				if verify {
					again = true // check Asana right away (once per push)
				} else {
					res.Failed++
				}
			}
		}
	}
	return res, nil
}

// maxStepsPerEntry bounds how often Push handles one entry (see Push).
const maxStepsPerEntry = 4

// waitsForCreate reports whether op belongs to a task (or, for a subtask's create, a
// parent) created offline whose create is still queued or failed. It waits instead of
// failing, and goes out once the create is sent (or is discarded with it).
func (s *Syncer) waitsForCreate(ctx context.Context, op store.Op) (bool, error) {
	gid := op.TaskGID
	switch op.Kind {
	case store.OpCreateTask:
		gid, _ = op.Payload["parent"].(string)
	case store.OpDuplicateTask, store.OpCreateStatus, store.OpRenameSection:
		return false, nil
	}
	if !store.IsLocalGID(gid) {
		return false, nil
	}
	real, err := s.Store.RealGID(ctx, gid)
	if err != nil || !store.IsLocalGID(real) {
		return false, err
	}
	return s.Store.CreateQueued(ctx, real)
}

// send sends one entry (already marked "sending").
func (s *Syncer) send(ctx context.Context, op store.Op) (outcome, error) {
	var err error
	switch op.Kind {
	case store.OpUpdateTask:
		return s.pushUpdate(ctx, op)
	case store.OpRenameSection:
		return s.pushRenameSection(ctx, op)
	case store.OpCreateTask:
		return s.pushCreate(ctx, op)
	case store.OpAddComment:
		err = s.pushComment(ctx, op)
	case store.OpMoveTask:
		err = s.pushMove(ctx, op)
	case store.OpAddTag, store.OpRemoveTag:
		err = s.pushTag(ctx, op)
	case store.OpAddFollower, store.OpRemoveFollower, store.OpAddProject, store.OpRemoveProject:
		err = s.pushRelation(ctx, op)
	case store.OpDuplicateTask:
		err = s.pushDuplicate(ctx, op)
	case store.OpUploadAttachment:
		err = s.pushUpload(ctx, op)
	case store.OpCreateStatus:
		err = s.pushStatus(ctx, op)
	default:
		err = &asana.Error{Status: 400}
	}
	return sent, err
}

// holdBack handles a network error while sending op: it goes back in the queue, to be
// verified first if Asana may have received it and sending it twice would duplicate it.
func (s *Syncer) holdBack(ctx context.Context, op store.Op, cause error) error {
	ctx = context.WithoutCancel(ctx) // record the state even if ctx was cancelled
	if requestNotSent(cause) || isRateLimited(cause) {
		return s.Store.ReleaseNotSent(ctx, op.ID)
	}
	safe, err := s.Store.SafeToResend(ctx, op)
	if err != nil {
		return err
	}
	if safe {
		return s.Store.ReleaseOp(ctx, op.ID)
	}
	return s.Store.MarkOpVerify(ctx, op.ID, "")
}

// handleError records an error other than a network error. A 4xx (Asana rejected the
// change) or a broken response to a harmless entry marks it failed. A 5xx or a broken
// response to a create, or a change Asana accepted but we could not save, may have been
// applied: the entry is verified (verify = true).
func (s *Syncer) handleError(ctx context.Context, op store.Op, cause error) (verify bool, err error) {
	var apiErr *asana.Error
	var notSaved *sentNotSavedError
	switch {
	case errors.As(cause, &notSaved):
		verify = true
	case errors.As(cause, &apiErr):
		verify = apiErr.Status >= 500
	default:
		safe, err := s.Store.SafeToResend(ctx, op)
		if err != nil {
			return false, err
		}
		verify = !safe
	}
	if verify && op.Attempts >= maxSendAttempts {
		// Asana keeps failing on this change (e.g. a server bug its data triggers): show it.
		return false, s.Store.MarkOpFailed(ctx, op.ID, fmt.Errorf("%d 回送っても Asana がエラーを返しました: %w", op.Attempts, cause))
	}
	if verify {
		return true, s.Store.MarkOpVerify(ctx, op.ID, cause.Error())
	}
	return false, s.Store.MarkOpFailed(ctx, op.ID, cause)
}

// maxSendAttempts is how many requests for one entry may fail with a 5xx (over all
// pushes) before it is marked failed for the user.
const maxSendAttempts = 5

func isNotFound(err error) bool {
	var apiErr *asana.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// dropGone handles a 404 for op: Asana no longer has what it changes. A 404 does not say
// which object is missing, so for a change to a task the task itself is looked up: if it
// is gone, the task and every change queued for it (and its subtasks) are dropped;
// otherwise only this change is (its section, project, tag, ... is gone). A create is
// dropped with its local task (its parent or project is gone). Dropped changes are
// recorded for the user and listed in res.Discarded.
func (s *Syncer) dropGone(ctx context.Context, op store.Op, cause error, res *PushResult) error {
	reason := "Asana で削除されています (" + cause.Error() + ")"
	now := s.clock()
	var discards []store.Discard
	var err error
	switch op.Kind {
	case store.OpCreateTask, store.OpDuplicateTask:
		discards, err = s.Store.DiscardDeletedTask(ctx, op.TaskGID, reason, now)
	case store.OpRenameSection, store.OpCreateStatus:
		var d store.Discard
		d, err = s.Store.DiscardGoneOp(ctx, op.ID, reason, now)
		discards = []store.Discard{d}
	default:
		task, rerr := s.Store.RealGID(ctx, op.TaskGID)
		if rerr != nil {
			return rerr
		}
		gone := store.IsLocalGID(task) // its create is not queued any more
		if !gone {
			_, perr := s.API.Task(ctx, task)
			switch {
			case isNotFound(perr):
				gone = true
			case perr != nil: // cannot tell what is missing: keep it for now
				if isNetworkError(perr) || isRateLimited(perr) {
					if err := s.Store.ReleaseOp(context.WithoutCancel(ctx), op.ID); err != nil {
						return err
					}
					return perr // offline: stop like any network error
				}
				res.Failed++
				return s.Store.MarkOpFailed(ctx, op.ID, cause)
			}
		}
		if gone {
			discards, err = s.Store.DiscardDeletedTask(ctx, task, reason, now)
		} else {
			var d store.Discard
			d, err = s.Store.DiscardGoneOp(ctx, op.ID, reason, now)
			discards = []store.Discard{d}
		}
	}
	if err != nil {
		return err
	}
	res.Discarded = append(res.Discarded, discards...)
	return nil
}

// isRejected reports whether Asana answered with a 4xx other than 429: the request itself
// is not acceptable (missing, forbidden, invalid), so repeating it will not help.
func isRejected(err error) bool {
	var apiErr *asana.Error
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != http.StatusTooManyRequests
}

// isRateLimited reports whether Asana still answered 429 after the client's retries: the
// request was not processed, so the entry is kept for the next push.
func isRateLimited(err error) bool {
	var apiErr *asana.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusTooManyRequests
}

// isNetworkError reports whether err means Asana could not be reached (or the request was
// cancelled), as opposed to Asana or the local data rejecting the change.
func isNetworkError(err error) bool {
	var netErr net.Error
	var urlErr *url.Error
	return errors.As(err, &netErr) || errors.As(err, &urlErr) ||
		errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// requestNotSent reports whether a network error happened before the request could reach
// the server (no connection could be made).
func requestNotSent(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "proxyconnect") {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

func (s *Syncer) pushUpdate(ctx context.Context, op store.Op) (outcome, error) {
	if store.IsLocalGID(op.TaskGID) {
		return sent, &asana.Error{Status: 404} // its create failed
	}
	server, err := s.API.Task(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	current := store.TaskFieldValues(server)
	conflicts := map[string]any{}
	for k, local := range op.Payload {
		if k == "html_notes" {
			continue // the server normalizes HTML; "notes" (plain) is compared instead
		}
		sv := current[k]
		if !same(sv, op.Base[k]) && !same(sv, local) {
			conflicts[k] = sv
		}
	}
	if len(conflicts) > 0 {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, conflicts)
	}
	t, err := s.API.UpdateTask(ctx, op.TaskGID, apiFields(op.Payload))
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	s.refresh(ctx, *t)
	return sent, nil
}

// refresh stores a task as Asana returned it after a change. The change is already
// recorded, so a failure here only leaves the cache stale until the next sync.
func (s *Syncer) refresh(ctx context.Context, t asana.Task) {
	_ = s.Store.UpsertTasks(ctx, []asana.Task{t})
}

func (s *Syncer) pushCreate(ctx context.Context, op store.Op) (outcome, error) {
	fields := apiFields(op.Payload)
	section, _ := fields["section"].(string)
	project, _ := fields["project"].(string)
	parent, _ := fields["parent"].(string)
	delete(fields, "section")
	delete(fields, "project")
	delete(fields, "parent")
	if project != "" {
		fields["projects"] = []string{project}
	}
	var t *asana.Task
	var err error
	if parent != "" {
		// The parent may have been created offline too; it has been sent before us.
		if parent, err = s.Store.RealGID(ctx, parent); err != nil {
			return sent, err
		}
		if store.IsLocalGID(parent) {
			return sent, &asana.Error{Status: 404} // the parent's create failed
		}
		delete(fields, "workspace")
		t, err = s.API.CreateSubtask(ctx, parent, fields)
	} else {
		t, err = s.API.CreateTask(ctx, fields)
	}
	if err != nil {
		return sent, err
	}
	// Moving the task into its section is a separate request: the entry becomes a move,
	// sent right after (and retried later if it fails), so the create is never repeated.
	var move *store.MoveTarget
	if section != "" {
		move = &store.MoveTarget{Project: project, Section: section, From: sectionIn(t, project)}
	}
	if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, t.GID, move); err != nil {
		return sent, sentNotSaved(err)
	}
	s.refresh(ctx, *t)
	if move != nil {
		return followUp, nil
	}
	return sent, nil
}

// sectionIn returns the section t is in within project ("" if unknown).
func sectionIn(t *asana.Task, project string) string {
	for _, m := range t.Memberships {
		if m.Project != nil && m.Project.GID == project && m.Section != nil {
			return m.Section.GID
		}
	}
	return ""
}

func (s *Syncer) pushComment(ctx context.Context, op store.Op) error {
	if store.IsLocalGID(op.TaskGID) {
		return &asana.Error{Status: 404} // its create failed
	}
	text, _ := op.Payload["text"].(string)
	htmlText, _ := op.Payload["html_text"].(string)
	localGID, _ := op.Payload["story_gid"].(string)
	st, err := s.API.AddComment(ctx, op.TaskGID, text, htmlText)
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteComment(ctx, op.ID, localGID, op.TaskGID, *st))
}

func (s *Syncer) pushMove(ctx context.Context, op store.Op) error {
	if store.IsLocalGID(op.TaskGID) {
		return &asana.Error{Status: 404} // its create failed
	}
	section, _ := op.Payload["section"].(string)
	if err := s.API.AddTaskToSection(ctx, section, op.TaskGID); err != nil {
		return err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sentNotSaved(err)
	}
	if t, err := s.API.Task(ctx, op.TaskGID); err == nil { // else the next sync refreshes it
		s.refresh(ctx, *t)
	}
	return nil
}

func (s *Syncer) pushRenameSection(ctx context.Context, op store.Op) (outcome, error) {
	server, err := s.API.Section(ctx, op.TaskGID)
	if err != nil {
		return sent, err
	}
	name, _ := op.Payload["name"].(string)
	if !same(server.Name, op.Base["name"]) && !same(server.Name, name) {
		return conflicted, s.Store.MarkOpConflict(ctx, op.ID, map[string]any{"name": server.Name})
	}
	updated, err := s.API.UpdateSection(ctx, op.TaskGID, name)
	if err != nil {
		return sent, err
	}
	if err := s.Store.CompleteOp(ctx, op.ID); err != nil {
		return sent, sentNotSaved(err)
	}
	_ = s.Store.ApplySectionName(ctx, op.TaskGID, updated.Name) // the next sync fixes a failure
	return sent, nil
}

func (s *Syncer) pushTag(ctx context.Context, op store.Op) error {
	if store.IsLocalGID(op.TaskGID) {
		return &asana.Error{Status: 404} // its create failed
	}
	tag, _ := op.Payload["tag"].(string)
	tag, err := s.Store.RealTagGID(ctx, tag)
	if err != nil {
		return err
	}
	if op.Kind == store.OpRemoveTag {
		if err := s.API.RemoveTag(ctx, op.TaskGID, tag); err != nil {
			return err
		}
		return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
	}
	if store.IsLocalGID(tag) { // created offline: create it in Asana first
		name, _ := op.Payload["tag_name"].(string)
		workspace, _ := op.Payload["workspace"].(string)
		created, err := s.API.CreateTag(ctx, workspace, name)
		if err != nil {
			return err
		}
		if err := s.Store.ReplaceLocalTagGID(ctx, tag, created.GID); err != nil {
			return sentNotSaved(err)
		}
		tag = created.GID
	}
	if err := s.API.AddTag(ctx, op.TaskGID, tag); err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

func (s *Syncer) pushRelation(ctx context.Context, op store.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if store.IsLocalGID(task) {
		return &asana.Error{Status: 404} // its create failed
	}
	user, _ := op.Payload["user"].(string)
	project, _ := op.Payload["project"].(string)
	section, _ := op.Payload["section"].(string)
	switch op.Kind {
	case store.OpAddFollower:
		err = s.API.AddFollowers(ctx, task, []string{user})
	case store.OpRemoveFollower:
		err = s.API.RemoveFollowers(ctx, task, []string{user})
	case store.OpAddProject:
		err = s.API.AddProject(ctx, task, project, section)
	case store.OpRemoveProject:
		err = s.API.RemoveProject(ctx, task, project)
	}
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteOp(ctx, op.ID))
}

func (s *Syncer) pushDuplicate(ctx context.Context, op store.Op) error {
	source, _ := op.Payload["source"].(string)
	name, _ := op.Payload["name"].(string)
	created, err := s.API.DuplicateTask(ctx, source, name)
	if err != nil {
		return err
	}
	if err := s.Store.CompleteCreate(ctx, op.ID, op.TaskGID, created.GID, nil); err != nil {
		return sentNotSaved(err)
	}
	if t, err := s.API.Task(ctx, created.GID); err == nil { // subtasks etc. follow on the next sync
		s.refresh(ctx, *t)
	}
	return nil
}

func (s *Syncer) pushUpload(ctx context.Context, op store.Op) error {
	task, err := s.Store.RealGID(ctx, op.TaskGID)
	if err != nil {
		return err
	}
	if store.IsLocalGID(task) {
		return &asana.Error{Status: 404} // its create failed
	}
	local, _ := op.Payload["attachment"].(string)
	name, _ := op.Payload["name"].(string)
	path, _ := op.Payload["path"].(string)
	data, err := os.ReadFile(path)
	if err != nil {
		return &asana.Error{Status: 400} // the copied file is gone: cannot be retried
	}
	att, err := s.API.UploadAttachment(ctx, task, name, data)
	if err != nil {
		return err
	}
	return sentNotSaved(s.Store.CompleteUpload(ctx, op.ID, local, att.GID, att.ViewURL))
}

// apiFields converts outbox values to Asana's: "" clears due_on / assignee.
func apiFields(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	if _, rich := payload["html_notes"]; rich {
		payload = maps.Clone(payload)
		delete(payload, "notes") // Asana derives the plain notes from html_notes
	}
	for k, v := range payload {
		if slices.Contains([]string{"due_on", "assignee"}, k) && v == "" {
			out[k] = nil
			continue
		}
		out[k] = v
	}
	return out
}

func same(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }
