package sync

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/domain/rich"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// ambiguousMessage is the last_error of an entry that may have reached Asana, when Asana
// has several candidates for what it created. Like the other messages Push records, it is
// text for the user, kept in the outbox as it was written (an older store shows the same).
const ambiguousMessage = "Asana に届いたか自動で確かめられませんでした（同じ内容のものが複数あります）。Asana で確認して、届いていなければ再送、届いていれば破棄してください"

// PushResult is what one push did (readmodel.PushResult, so that internal/app can read it).
type PushResult = readmodel.PushResult

// ErrPushBusy is returned by Push when another process (TUI, CLI, MCP server) is sending
// the outbox. The entries stay queued; they are sent by that process or the next push.
var ErrPushBusy = readmodel.ErrPushBusy

// ErrPullOnly is returned by Push on a Syncer with PullOnly set (the read-only MCP server).
var ErrPullOnly = errors.New("read-only: queued changes are not sent")

// outcome is what happened to an entry Asana accepted.
type outcome int

const (
	sent       outcome = iota
	conflicted         // left for the user to resolve
	followUp           // sent; the entry now holds a follow-up change to send right away
	dropped            // not sent and removed (e.g. Asana refuses read / unread changes)
)

// sentNotSavedError wraps a failure to record a change that Asana already accepted.
type sentNotSavedError struct{ err error }

func (e *sentNotSavedError) Error() string {
	return "Asana には送信済みですが、ローカルへの反映に失敗しました: " + e.err.Error()
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
//
// Changes to tasks that already exist in Asana, next to each other in the queue, go out
// up to 10 per request (POST /batch); each is then handled as if sent alone (batch.go).
func (s *Pusher) Push(ctx context.Context) (res PushResult, err error) {
	if s.PullOnly {
		return res, ErrPullOnly
	}
	unlock, err := s.lock(ctx, &res)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := s.checkPushAccount(ctx); err != nil {
		res.Remaining, _, _, _ = s.Store.OutboxCounts(context.WithoutCancel(ctx))
		return res, err
	}
	res, _, err = s.pushLocked(ctx, &dropState{})
	return res, err
}

// checkPushAccount makes sure, before Push sends anything, that the API acts as the user
// the local data was synced with: changes queued by one account must never be sent as
// another (after a login as someone else). Run checks this itself at its start. Before the
// first sync there is no account to compare with. Once confirmed, it is not asked again
// for this Syncer: its token source never switches to another user (auth.ErrLoginReplaced).
func (s *Pusher) checkPushAccount(ctx context.Context) error {
	if s.accountConfirmed() {
		return nil
	}
	want, err := s.Store.SyncedMeGID(ctx)
	if err != nil || want == "" {
		return err
	}
	me, err := s.API.Me(ctx)
	if err != nil {
		return err // offline or logged out: nothing could be sent anyway
	}
	if me.GID != want {
		return fmt.Errorf("%w (%s / %s)", ErrAccountChanged, me.GID, want)
	}
	s.confirmAccount()
	return nil
}

// lock takes the store's push lock (held by Push, by all of Run, and by store.Reset).
func (s *Pusher) lock(ctx context.Context, res *PushResult) (func(), error) {
	unlock, err := s.Store.TryLockPush()
	if errors.Is(err, readmodel.ErrPushBusy) {
		res.Busy = true
		res.Remaining, _, _, _ = s.Store.OutboxCounts(ctx)
	}
	return unlock, err
}

// pushLocked is Push for a caller holding the push lock. Changes queued by other
// processes while it sends (they got ErrPushBusy and were told this process sends them)
// are sent too, by one more pass before it returns. last is the newest entry handled.
//
// drops is shared by every pass under the same lock (one per Push, one per Run), so the
// account is checked once and maxDroppedTasks bounds the whole push or sync.
func (s *Pusher) pushLocked(ctx context.Context, drops *dropState) (res PushResult, last int64, err error) {
	if res, last, err = s.pushPass(ctx, 0, drops); err != nil {
		return res, last, err
	}
	return s.catchUp(ctx, res, last, drops)
}

// catchUp sends entries queued after entry last (one more pass) and adds its result.
func (s *Pusher) catchUp(ctx context.Context, res PushResult, last int64, drops *dropState) (PushResult, int64, error) {
	more, last, err := s.pushPass(ctx, last, drops)
	res.Sent += more.Sent
	res.Conflicts += more.Conflicts
	res.Failed += more.Failed
	res.Discarded = append(res.Discarded, more.Discarded...)
	res.InboxReadOnly = res.InboxReadOnly || more.InboxReadOnly
	res.Remaining = more.Remaining
	return res, last, err
}

// pushPass sends the pending entries newer than entry after, in order.
func (s *Pusher) pushPass(ctx context.Context, after int64, drops *dropState) (res PushResult, last int64, err error) {
	last = after
	if _, _, err := s.Store.RecoverInterruptedOps(ctx); err != nil {
		return res, last, err
	}
	// comments and status updates queued before #63 are sent as they are converted now (#71)
	var fit func(rich.Target, string) (string, string, bool)
	if s.RichText != nil {
		fit = s.RichText.Fit
	}
	if _, err := s.Store.FitQueuedRichText(ctx, fit); err != nil {
		return res, last, err
	}
	all, err := s.Store.PendingOps(ctx)
	if err != nil {
		return res, last, err
	}
	var ops []outbox.Op
	for _, op := range all {
		if op.ID > after {
			ops = append(ops, op)
			last = max(last, op.ID)
		}
	}
	defer func() { // report what is still queued (offline, waiting, or to be verified)
		res.Remaining, _, _, _ = s.Store.OutboxCounts(context.WithoutCancel(ctx))
	}()
	verified := map[int64]bool{} // checked in Asana during this push (at most once each)
	// The account is checked only before dropping anything (dropGone), not here: only Run
	// changes the stored account (SyncedMeGID/SetMeGID), and Run refuses to when Asana answers as
	// another account, so the stored one stays the account the local data belongs to.
	drops.res = &res // this pass's counts
	for i := 0; i < len(ops); {
		if err := ctx.Err(); err != nil {
			return res, last, err
		}
		// Updates to tasks that already exist in Asana, one after another in the queue, go
		// out together (POST /batch, see batch.go).
		group, err := s.batchGroup(ctx, ops[i:])
		if err != nil {
			return res, last, err
		}
		if len(group) >= 2 {
			again, err := s.pushBatch(ctx, group, &res, drops)
			if err != nil {
				return res, last, err
			}
			for _, b := range group {
				if again[b.op.ID] { // e.g. check Asana right away after a 5xx, as for one entry
					if err := s.pushEntry(ctx, b.op, 1, verified, &res, drops); err != nil {
						return res, last, err
					}
				}
			}
			i += len(group)
			continue
		}
		if err := s.pushEntry(ctx, ops[i], 0, verified, &res, drops); err != nil {
			return res, last, err
		}
		i++
	}
	return res, last, nil
}

// pushEntry handles one entry (snapshot as listed when the pass began), from step first
// on (a step already taken by pushBatch counts). An error stops the push.
func (s *Pusher) pushEntry(ctx context.Context, snapshot outbox.Op, first int, verified map[int64]bool, res *PushResult, drops *dropState) error {
	// An entry is handled a bounded number of times per push, whatever happens: send,
	// check + send again, the follow-up move of a create, and that move's retry. What is
	// left over waits for the next push.
	for step, again := first, true; again && step < maxStepsPerEntry; step++ {
		again = false
		if err := ctx.Err(); err != nil {
			return err
		}
		// Re-read: sending an earlier entry may have rewritten this one (a create replaces
		// the local gid that later comments/updates refer to).
		op, err := s.Store.GetOp(ctx, snapshot.ID)
		if errors.Is(err, readmodel.ErrNotFound) {
			break
		}
		if err != nil {
			return err
		}
		if op.State != outbox.StatePending || (op.Verify && verified[op.ID]) {
			break // sent again during this push already: verify on the next one
		}
		if held, err := s.holdUnreadable(ctx, *op); err != nil {
			return err
		} else if held {
			break
		}
		if wait, err := s.waitsForCreate(ctx, *op); err != nil {
			return err
		} else if wait {
			break
		}
		if ok, err := s.Store.BeginSend(ctx, op.ID, s.clock()); err != nil {
			return err
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
				if err := s.dropGone(ctx, *op, err, drops); err != nil {
					return err
				}
				sendIt = false
			case model.IsUnauthorized(err): // the token is rejected: keep it for after a new login
				if rerr := s.Store.MarkOpVerify(context.WithoutCancel(ctx), op.ID, "Asana に届いたか確かめられませんでした: "+err.Error()); rerr != nil {
					return rerr
				}
				return err
			case isRejected(err): // e.g. not ours (403)
				if err := s.Store.MarkOpFailed(ctx, op.ID, fmt.Errorf("Asana に届いたか確かめる先にアクセスできません: %w", err)); err != nil {
					return err
				}
				res.Failed++
				sendIt = false
			case err != nil: // could not check: try again on the next push
				note := "Asana に届いたか確かめられませんでした: " + err.Error()
				if rerr := s.Store.MarkOpVerify(context.WithoutCancel(ctx), op.ID, note); rerr != nil {
					return rerr
				}
				if isNetworkError(err) || isRateLimited(err) {
					return err
				}
				sendIt = false
			case m == tooEarly: // Asana may not list it yet: check again on the next push
				if err := s.Store.MarkOpVerify(ctx, op.ID, "Asana への反映を待っています"); err != nil {
					return err
				}
				sendIt = false
			case m == ambiguous:
				if err := s.Store.MarkOpFailed(ctx, op.ID, errors.New(ambiguousMessage)); err != nil {
					return err
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
					return err
				}
			}
		}
		if !sendIt {
			continue
		}
		if err := s.Store.CountAttempt(ctx, op.ID, s.clock()); err != nil {
			return err
		}
		op.Attempts++
		if h := handlers[op.Kind]; h.sendAlong != nil {
			var batched int // entries sent along with this one, whatever happened to it
			out, batched, err = h.sendAlong(s, ctx, *op)
			res.Sent += batched
		} else {
			out, err = s.send(ctx, *op)
		}
		if again, err = s.handleOutcome(ctx, *op, out, err, wasFollowUp, res, drops); err != nil {
			return err
		}
	}
	return nil
}

// handleOutcome records what sending op (already marked "sending") gave: out, or the
// error err. again: handle the entry once more right away (a follow-up, or a check of
// Asana). An error stops the push (offline, logged out, the store failing); op has been
// put back in the queue as it should then.
func (s *Pusher) handleOutcome(ctx context.Context, op outbox.Op, out outcome, err error, wasFollowUp bool, res *PushResult, drops *dropState) (again bool, stop error) {
	switch {
	case err == nil:
		switch {
		case out == conflicted:
			res.Conflicts++
		case out == dropped:
			res.InboxReadOnly = true
		case !wasFollowUp:
			res.Sent++ // a follow-up is part of the change already counted
		}
		return out == followUp, nil
	case isNetworkError(err) || isRateLimited(err) || model.IsUnauthorized(err):
		if err := s.holdBack(ctx, op, err); err != nil {
			return false, err
		}
		return false, err // offline, cancelled or logged out: keep the rest for later
	case isNotFound(err): // what it changes was deleted in Asana
		return false, s.dropGone(ctx, op, err, drops)
	case errors.As(err, new(*unreadableError)): // not sent: held for a version that reads it
		return false, s.holdBackUnreadable(ctx, op, err)
	case errors.As(err, new(*customTypeWithdrawn)): // Asana did not take the custom type (#59)
		var w *customTypeWithdrawn
		errors.As(err, &w)
		return s.withdrawCustomType(ctx, op, w, res)
	case errors.As(err, new(*startOnIgnored)): // done, but Asana did not keep the start date (#62)
		var w *startOnIgnored
		errors.As(err, &w)
		d, rerr := s.Store.RecordStartOnIgnored(ctx, w.task, w.start, w.reason(), s.clock())
		if rerr != nil {
			return false, rerr
		}
		res.Discarded = append(res.Discarded, d)
		if !wasFollowUp {
			res.Sent++
		}
		return out == followUp, w.markErr // the store failing stops the push
	case errors.As(err, new(*startOnWithdrawn)): // the plan or the due date does not allow the start date
		var w *startOnWithdrawn
		errors.As(err, &w)
		return s.withdrawStartOn(ctx, op, w, res)
	default:
		verify, ferr := s.handleError(ctx, op, err)
		if ferr != nil {
			return false, ferr
		}
		if !verify {
			res.Failed++
		}
		return verify, nil // verify: check Asana right away (once per push)
	}
}

// maxStepsPerEntry bounds how often Push handles one entry (see Push).
const maxStepsPerEntry = 4

// waitsForCreate reports whether op belongs to a task (or, for a subtask's create or a
// parent change, a parent) created offline whose create is still queued or failed. It
// waits instead of failing, and goes out once the create is sent (or is discarded with
// it). An edit or delete of a comment or attachment not created in Asana yet waits for
// its create too.
func (s *Pusher) waitsForCreate(ctx context.Context, op outbox.Op) (bool, error) {
	h := handlers[op.Kind]
	if h.local != nil {
		if g, result, ok := h.local(op); ok {
			if _, waiting, err := s.Store.LocalResult(ctx, result, g); err != nil || waiting {
				return waiting, err
			}
		}
	}
	if wait, err := s.waitsForSection(ctx, op); err != nil || wait {
		return wait, err
	}
	if wait, err := s.waitsForField(ctx, op); err != nil || wait {
		return wait, err
	}
	if h.waits != nil {
		if wait, err := h.waits(s, ctx, op); err != nil || wait {
			return wait, err
		}
	}
	for _, gid := range outbox.TaskDeps(op) {
		if wait, err := s.createQueued(ctx, gid); err != nil || wait {
			return wait, err
		}
	}
	return false, nil
}

// createQueued reports whether gid is a task created offline whose create (or duplicate)
// is still in the outbox, in any state.
func (s *Pusher) createQueued(ctx context.Context, gid string) (bool, error) {
	if !localgid.IsLocal(gid) {
		return false, nil
	}
	real, err := s.Store.RealGID(ctx, gid)
	if err != nil || !localgid.IsLocal(real) {
		return false, err
	}
	return s.Store.CreateQueued(ctx, real)
}

// send sends one entry (already marked "sending"). An entry Asana refuses because the
// workspace's plan lacks the feature (402) also hides that feature in the workspace for
// plan.RecheckAfter.
func (s *Pusher) send(ctx context.Context, op outbox.Op) (outcome, error) {
	h := handlers[op.Kind]
	var start string
	var withStart bool
	if h.startOn != nil {
		start, withStart = h.startOn(op)
	}
	var startWS string
	wsKnown := false
	if withStart {
		// before sending: a create gives the task its Asana gid
		var err error
		startWS, err = s.Store.TaskWorkspace(ctx, op.TaskGID)
		wsKnown = err == nil // not in the cache: nothing to record the plan for
	}
	out, err := s.sendOp(ctx, op)
	if isForbidden(err) && h.forbidden != nil {
		err = h.forbidden(s, ctx, op, err)
	}
	if feature := plan.FeatureOf(op.Kind); feature == plan.CustomFields { // recorded per workspace
		ws := s.planWorkspace(ctx, op)
		if err == nil {
			return out, s.Store.ClearPlanRefusal(ctx, feature, ws)
		}
		if refused, rerr := recordPlanRefusal(ctx, s.Store, s.clock(), feature, ws, err); rerr != nil {
			return out, rerr
		} else if refused {
			err = fmt.Errorf("%s: %w", readmodel.PlanUnavailableMessage(feature), err)
		}
		return out, err
	}
	if withStart && !errors.As(err, new(*customTypeWithdrawn)) {
		// A 402 to an entry with a custom type takes out the custom type first (#59),
		// the more likely cause: the start date is tried again with the rest.
		if isMilestoneStartRefused(err) {
			// Not the plan: no mark. The start date is taken out and the rest sent again.
			return out, &startOnWithdrawn{reason: milestoneStartReason(start), err: err}
		}
		if wsKnown {
			return out, s.startOnOutcome(ctx, startWS, start, out, err)
		}
	}
	switch feature := plan.FeatureOf(op.Kind); feature {
	case plan.TimeTracking:
		if model.IsPaymentRequired(err) {
			if ws, wsErr := s.Store.TaskWorkspace(ctx, op.TaskGID); wsErr == nil {
				_, _ = recordPlanRefusal(ctx, s.Store, s.clock(), feature, ws, err) // per workspace, for a while (plan.go)
			}
		}
	case plan.Sections, plan.Tags, plan.MySection: // per workspace, for a while (#50)
		switch {
		case err == nil && out != conflicted:
			// Asana took it: a failure to forget the 402 only hides the feature until it expires.
			_ = s.Store.ClearPlanRefusal(ctx, feature, s.planWorkspace(ctx, op))
		case model.IsPaymentRequired(err):
			_, _ = recordPlanRefusal(ctx, s.Store, s.clock(), feature, s.planWorkspace(ctx, op), err)
		}
	}
	// (the brief's 402 is recorded per workspace by brief.go)
	return out, err
}

// startOnOutcome records what sending a task's start date start (of an update or create
// with start_on, "" when it clears it) to workspace ws said about its plan: a 402 hides start dates there for a while
// (plan.StartOn), and the error says why; an answer that set one forgets an
// earlier 402. err is returned, wrapped when it is a 402.
func (s *Pusher) startOnOutcome(ctx context.Context, ws, start string, out outcome, err error) error {
	var ignored *startOnIgnored
	if errors.As(err, &ignored) {
		// Taken for a 402: start dates are hidden in ws for a while, as after one.
		ignored.markErr = s.Store.RecordPlanRefusal(ctx, plan.StartOn, ws, s.clock())
		return err
	}
	if err == nil {
		if start != "" && out != conflicted {
			// Sent: a failure to forget the 402 only hides start dates until it expires.
			_ = s.Store.ClearPlanRefusal(ctx, plan.StartOn, ws)
		}
		return nil
	}
	if refused, rerr := recordPlanRefusal(ctx, s.Store, s.clock(), plan.StartOn, ws, err); rerr != nil {
		return errors.Join(err, rerr)
	} else if refused {
		return &startOnWithdrawn{reason: readmodel.PlanUnavailableMessage(plan.StartOn), err: err}
	}
	return err
}

// isMilestoneStartRefused reports Asana's 400 to a start date on a milestone, on any plan
// (measured on the real Asana, #62: an update and a create answer it alike).
func isMilestoneStartRefused(err error) bool {
	var e *model.Error
	if !errors.As(err, &e) || e.Status != http.StatusBadRequest {
		return false
	}
	for _, x := range e.Errors {
		if x.Error == "cannot_set_start_date_on_milestone" {
			return true
		}
	}
	return false
}

// milestoneStartReason is why a start date sent to a milestone was taken out.
func milestoneStartReason(start string) string {
	return fmt.Sprintf("マイルストーンには開始日を付けられないため、開始日 (%s) を外しました (Asana が 400 を返しました)", start)
}

// startOnIgnored says that Asana answered a create or an update that set a task's start
// date (to start) with the task keeping another one (kept, "" for none), as a plan
// without start dates may do instead of a 402 (#62). The rest of the change was applied
// and the entry is done; the start date is recorded as a dropped change, with the reason
// a 402 gives (handleOutcome), and start dates are hidden in the workspace for a while
// (startOnOutcome). The cache has the task as Asana answered it.
type startOnIgnored struct {
	task    string // the task's gid in Asana
	start   string
	kept    string
	markErr error // recording the mark failed (startOnOutcome)
}

func (e *startOnIgnored) reason() string { return readmodel.PlanUnavailableMessage(plan.StartOn) }

func (e *startOnIgnored) Error() string {
	return fmt.Sprintf("%s (Asana が開始日 %s を付けませんでした)", e.reason(), e.start)
}

// startOnWithdrawn says that an entry's start date cannot be sent (Asana answered 402,
// or 400 to a start date on a milestone, or the task's due date in Asana no longer allows
// it) and why: the start date is taken
// out of the entry and the rest sent again (handleOutcome, withdrawStartOn).
type startOnWithdrawn struct {
	reason string
	err    error // Asana's answer (nil when nothing was sent)
}

func (e *startOnWithdrawn) Error() string {
	if e.err == nil {
		return e.reason
	}
	return e.reason + ": " + e.err.Error()
}
func (e *startOnWithdrawn) Unwrap() error { return e.err }

// withdrawStartOn handles a start date that cannot be sent without making the user
// decide: the start date is dropped (and reported in res.Discarded, with the reason), and
// the rest of the entry, if any, is sent again right away. Asana did not apply the
// request (a 402 or a 400, or it was not sent), so sending the rest again repeats nothing.
func (s *Pusher) withdrawStartOn(ctx context.Context, op outbox.Op, w *startOnWithdrawn, res *PushResult) (again bool, err error) {
	rest, d, err := s.Store.WithdrawStartOn(ctx, op.ID, w.reason, s.clock())
	if err != nil {
		return false, err
	}
	res.Discarded = append(res.Discarded, d)
	return rest != nil, nil
}

func (s *Pusher) sendOp(ctx context.Context, op outbox.Op) (outcome, error) {
	h, ok := handlers[op.Kind]
	if !ok { // an entry of a newer version (held before it gets here: holdUnreadable)
		return sent, &unreadableError{err: outbox.Readable(op)}
	}
	return h.send(s, ctx, op)
}

// holdBack handles a network error while sending op: it goes back in the queue, to be
// verified first if Asana may have received it and sending it twice would duplicate it.
// A 429 or a 401 (the token is rejected, e.g. an expired PAT) was not processed: the entry
// waits, unchanged, for the next push (after the user logs in again).
func (s *Pusher) holdBack(ctx context.Context, op outbox.Op, cause error) error {
	ctx = context.WithoutCancel(ctx) // record the state even if ctx was cancelled
	if requestNotSent(cause) || isRateLimited(cause) || model.IsUnauthorized(cause) {
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
func (s *Pusher) handleError(ctx context.Context, op outbox.Op, cause error) (verify bool, err error) {
	var apiErr *model.Error
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
	var apiErr *model.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// dropGone handles a 404 for op: Asana no longer has what it changes. A 404 does not say
// which object is missing, so for a change to a task the task itself is looked up: if it
// is gone, the task and every change queued for it (and its subtasks) are dropped;
// otherwise only this change is (its section, project, tag, ... is gone). A create is
// dropped with its local task (its parent or project is gone). Dropped changes are
// recorded for the user and listed in res.Discarded.
func (s *Pusher) dropGone(ctx context.Context, op outbox.Op, cause error, st *dropState) error {
	res := st.res
	// Asana also answers 404 when the logged-in account cannot see the object (another
	// account or organization than the one the store was synced with). Make sure it is
	// the same user before dropping anything.
	if err := s.checkAccount(ctx, st); err != nil {
		if isTemporary(err) || model.IsUnauthorized(err) {
			if rerr := s.Store.ReleaseOp(context.WithoutCancel(ctx), op.ID); rerr != nil {
				return rerr
			}
			return err
		}
		if ferr := s.Store.MarkOpFailed(ctx, op.ID, err); ferr != nil {
			return ferr
		}
		res.Failed++
		return err // stop: every other change would hit the same problem
	}
	reason := "Asana で削除されています (" + cause.Error() + ")"
	now := s.clock()
	var discards []readmodel.Discard
	var err error
	deleteTask := func(gid string) error {
		if st.tasks >= maxDroppedTasks { // a second safeguard against dropping too much at once
			res.Failed++
			return s.Store.MarkOpFailed(ctx, op.ID, fmt.Errorf(
				"1回の同期で削除済みとして消すタスクの上限 (%d 件) を超えたため、残しました。Asana で確認してください: %w", maxDroppedTasks, cause))
		}
		st.tasks++
		discards, err = s.Store.DiscardDeletedTask(ctx, gid, reason, now)
		return nil
	}
	subject := outbox.SubjectOf(op.Kind)
	switch {
	case subject == outbox.SubjectNewTask: // its parent or project is gone
		if ferr := deleteTask(op.TaskGID); ferr != nil {
			return ferr
		}
	case subject != outbox.SubjectTask && op.Kind.Known(): // a section, project, tag, ... is gone
		var d readmodel.Discard
		d, err = s.Store.DiscardGoneOp(ctx, op.ID, reason, now)
		discards = []readmodel.Discard{d}
	default:
		task, rerr := s.Store.RealGID(ctx, op.TaskGID)
		if rerr != nil {
			return rerr
		}
		gone := localgid.IsLocal(task) // its create is not queued any more
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
			if ferr := deleteTask(task); ferr != nil {
				return ferr
			}
		} else {
			// Something else it names is missing, or private to someone else.
			if f := handlers[op.Kind].goneReason; f != nil {
				if r := f(op, cause); r != "" {
					reason = r
				}
			}
			var d readmodel.Discard
			d, err = s.Store.DiscardGoneOp(ctx, op.ID, reason, now)
			discards = []readmodel.Discard{d}
		}
	}
	if err != nil {
		return err
	}
	res.Discarded = append(res.Discarded, discards...)
	return nil
}

// dropState is what dropGone keeps during one push.
type dropState struct {
	res     *PushResult // the current pass's result
	checked bool        // the account was checked
	account error       // why changes must not be dropped (nil: same account)
	tasks   int         // tasks dropped so far
}

// maxDroppedTasks is how many tasks one push may drop as deleted in Asana.
const maxDroppedTasks = 5

// ErrAccountChanged is returned by Push when the logged-in Asana account is not the one
// the local data belongs to: Asana answers 404 for everything, which must not be taken as
// deletions.
var ErrAccountChanged = readmodel.ErrAccountChanged

// checkAccount makes sure (once per push) that the API acts as the user the store was
// synced with.
func (s *Pusher) checkAccount(ctx context.Context, st *dropState) error {
	if st.checked {
		return st.account
	}
	if s.accountConfirmed() { // confirmed by this Syncer already (Push or Run)
		st.checked = true
		return nil
	}
	me, err := s.API.Me(ctx)
	if err != nil {
		if isTemporary(err) {
			return err // not checked: try again next time
		}
		if model.IsUnauthorized(err) {
			return err // the token is rejected (e.g. an expired PAT): log in again, not reset
		}
		st.checked, st.account = true, fmt.Errorf("%w (%v)", ErrAccountChanged, err)
		return st.account
	}
	want, err := s.Store.SyncedMeGID(ctx)
	if err != nil {
		return err
	}
	st.checked = true
	if me.GID != want { // also when nothing was synced yet (want == "")
		st.account = fmt.Errorf("%w (%s / %s)", ErrAccountChanged, me.GID, want)
	}
	return st.account
}

// isTemporary reports whether err may go away by itself (offline, rate limit, 5xx).
func isTemporary(err error) bool {
	var apiErr *model.Error
	return isNetworkError(err) || isRateLimited(err) || (errors.As(err, &apiErr) && apiErr.Status >= 500)
}

// isRejected reports whether Asana answered with a 4xx other than 429: the request itself
// is not acceptable (missing, forbidden, invalid), so repeating it will not help.
func isRejected(err error) bool {
	var apiErr *model.Error
	return errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != http.StatusTooManyRequests
}

// isRateLimited reports whether Asana still answered 429 after the client's retries: the
// request was not processed, so the entry is kept for the next push.
func isRateLimited(err error) bool {
	var apiErr *model.Error
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
// the server (no connection could be made, or no token: logged out, refresh failed).
// An error without a token says so itself (auth.TokenError's RequestNotSent).
func requestNotSent(err error) bool {
	var noToken interface{ RequestNotSent() bool }
	if errors.As(err, &noToken) && noToken.RequestNotSent() {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && (opErr.Op == "dial" || opErr.Op == "proxyconnect") {
		return true
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}

// apiFields converts outbox values to Asana's: "" clears due_on / start_on / assignee.
func apiFields(payload map[string]any) map[string]any {
	out := make(map[string]any, len(payload))
	if _, rich := payload["html_notes"]; rich {
		payload = maps.Clone(payload)
		delete(payload, "notes") // Asana derives the plain notes from html_notes
	}
	for k, v := range payload {
		if slices.Contains([]string{"due_on", "start_on", "assignee"}, k) && v == "" {
			out[k] = nil
			continue
		}
		out[k] = v
	}
	return out
}

// same compares a server value with one from the store. The store hands out text without
// terminal control characters (termsafe), so the server's is compared the same way;
// otherwise a task whose server value has one would conflict on every push.
func same(a, b any) bool {
	return termsafe.String(fmt.Sprint(a)) == termsafe.String(fmt.Sprint(b))
}
