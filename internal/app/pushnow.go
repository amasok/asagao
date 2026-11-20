package app

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/readmodel"
)

// DefaultPushTimeout is how long PushNow waits for Asana: a write command or tool should
// not hang on a slow network, the change is saved and the next sync sends it.
const DefaultPushTimeout = 20 * time.Second

// Pusher sends the outbox. *sync.Syncer satisfies it.
type Pusher interface {
	Push(ctx context.Context) (readmodel.PushResult, error)
}

// PushStore is the part of the local cache PushNow reads to give the Asana gids of what
// a write made offline. *store.Store satisfies it.
type PushStore interface {
	RealGID(ctx context.Context, gid string) (string, error)
	RealSectionGID(ctx context.Context, gid string) (string, error)
	LocalResult(ctx context.Context, kind readmodel.ResultKind, gid string) (real string, waiting bool, err error)
}

// PushOutcome is how a push right after a write went, for the entry points to word.
type PushOutcome int

const (
	// PushSent: everything queued reached Asana.
	PushSent PushOutcome = iota
	// PushNotConnected: there is no connection to Asana (Err: why, e.g. offline mode).
	PushNotConnected
	// PushNotLoggedIn: there is no connection because nobody is logged in (LoginHelp).
	PushNotLoggedIn
	// PushLoginRejected: Asana rejected the login token during the push (LoginHelp).
	PushLoginRejected
	// PushAccountChanged: another Asana account is logged in than the one the local data
	// was synced with, so nothing is sent (sync.ErrAccountChanged).
	PushAccountChanged
	// PushBusy: another process is sending the outbox and will send this change too.
	PushBusy
	// PushOffline: the push failed (Err), Asana could not be reached.
	PushOffline
	// PushInboxReadOnly: Asana refused to mark notifications read / unread, and the change
	// was undone.
	PushInboxReadOnly
	// PushIncomplete: the push ran, but conflicts, failures or entries still queued
	// (offline, waiting, to be verified) are left.
	PushIncomplete
)

// PushTarget names what the write made or changed, so that PushNow can tell whether that
// change was dropped and give the Asana gids of what it made offline.
type PushTarget struct {
	// TaskGIDs are the tasks the write changed (or made: a local gid).
	TaskGIDs []string
	// SectionGID is a section the write made.
	SectionGID string
	// TimeEntryGID is a time tracking entry the write recorded.
	TimeEntryGID string
}

// PushReport is what PushNow found. The entry points only word it.
type PushReport struct {
	Outcome PushOutcome
	// Err is the error that stopped the push (or the connection), nil when it ran.
	Err error
	// LoginHelp is what to do to log in (PushNotLoggedIn, PushLoginRejected).
	LoginHelp string
	// Result is what the push did; nil when it did not run (no connection).
	Result *readmodel.PushResult
	// Sent is true when the change reached Asana: the push sent everything and did not
	// drop the change.
	Sent bool
	// Dropped is set when the change was discarded because its task was deleted in Asana.
	Dropped bool
	// TaskGIDs, SectionGID and TimeEntryGID are the target's, with the Asana gid in place
	// of a local one once it was created.
	TaskGIDs     []string
	SectionGID   string
	TimeEntryGID string

	given []string // the target's task gids as the write gave them
}

// PushNow sends the outbox right after a write (the CLI's write commands, the MCP
// server's write tools). Failing to send is not an error of the write: the change is
// saved locally and the next sync sends it, so Run reports it instead.
type PushNow struct {
	Store PushStore
	// LoginHelp says what to do when err means logging in (again) (auth.LoginHelp); ""
	// for any other error. Nil: no error is taken as a login problem.
	LoginHelp func(err error) string
	// Timeout bounds connecting and pushing (0: DefaultPushTimeout).
	Timeout time.Duration
}

// Run connects (connect), pushes, sorts out what happened and resolves the target's
// gids. connect is given the context with the timeout; a connection kept for later calls
// should be made with a context of its own.
func (p PushNow) Run(ctx context.Context, connect func(context.Context) (Pusher, error), t PushTarget) PushReport {
	t.TaskGIDs = slices.DeleteFunc(slices.Clone(t.TaskGIDs), func(g string) bool { return g == "" })
	rep := PushReport{given: t.TaskGIDs}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultPushTimeout
	}
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	pusher, err := connect(pctx)
	if err != nil {
		rep.Err = err
		rep.Outcome = PushNotConnected
		if help := p.loginHelp(err); help != "" {
			rep.Outcome, rep.LoginHelp = PushNotLoggedIn, help
		}
	} else {
		res, err := pusher.Push(pctx)
		rep.Result, rep.Err = &res, err
		rep.Outcome = p.classify(res, err)
		if rep.Outcome == PushLoginRejected {
			rep.LoginHelp = p.loginHelp(err)
		}
	}
	rep.Sent = rep.Outcome == PushSent
	// Read the gids with ctx, not pctx: the store answers even when Asana timed out.
	rep.resolve(ctx, p.Store, t)
	if rep.Result != nil {
		rep.Drop(rep.Result.Discarded)
	}
	return rep
}

// classify sorts out a push's result. InboxReadOnly comes first: it says for sure that
// the change (marking notifications) was undone, whatever stopped the push after it.
func (p PushNow) classify(res readmodel.PushResult, err error) PushOutcome {
	switch {
	case res.InboxReadOnly:
		return PushInboxReadOnly
	case errors.Is(err, readmodel.ErrAccountChanged):
		return PushAccountChanged
	case err != nil && p.loginHelp(err) != "":
		return PushLoginRejected
	case errors.Is(err, readmodel.ErrPushBusy):
		return PushBusy
	case err != nil:
		return PushOffline
	case res.Conflicts > 0 || res.Failed > 0 || res.Remaining > 0:
		return PushIncomplete
	}
	return PushSent
}

func (p PushNow) loginHelp(err error) string {
	if p.LoginHelp == nil {
		return ""
	}
	return p.LoginHelp(err)
}

// resolve fills the report's gids: the Asana gid in place of a local one once created.
// A gid that can not be read stays as given.
func (r *PushReport) resolve(ctx context.Context, st PushStore, t PushTarget) {
	r.TaskGIDs = make([]string, len(t.TaskGIDs))
	for i, g := range t.TaskGIDs {
		r.TaskGIDs[i] = g
		if gid.IsLocal(g) {
			if real, err := st.RealGID(ctx, g); err == nil && real != "" {
				r.TaskGIDs[i] = real
			}
		}
	}
	r.SectionGID = t.SectionGID
	if gid.IsLocal(t.SectionGID) {
		if real, err := st.RealSectionGID(ctx, t.SectionGID); err == nil && real != "" {
			r.SectionGID = real
		}
	}
	// A sent time entry has its real gid, as add_time_entry gives it (found on the real
	// Asana, #31): the local one can not be used to edit or delete it.
	r.TimeEntryGID = t.TimeEntryGID
	if gid.IsLocal(t.TimeEntryGID) {
		if real, waiting, err := st.LocalResult(ctx, readmodel.ResultTimeEntry, t.TimeEntryGID); err == nil && real != "" && !waiting {
			r.TimeEntryGID = real
		}
	}
}

// Drop marks the change as dropped (and not sent) when one of ds discarded a change to
// one of the target's tasks, by the gid the write gave. A task the write created has a
// local gid there: its create was dropped. A discard by its Asana gid is a follow-up
// (e.g. the move into a section deleted in Asana) of a create that did reach Asana, so the
// write still counts as sent (the discard is reported on its own). A write that names no
// task (a section, a tag, a project) can not be told from another such change, so any
// discarded change without a task counts as its own. Run calls Drop with the push's
// discards; an entry point that shows the discards not shown yet (dropped by an earlier
// push, or by another process's) calls it with those too.
func (r *PushReport) Drop(ds []readmodel.Discard) {
	for _, d := range ds {
		mine := slices.Contains(r.given, d.TaskGID)
		if len(r.given) == 0 {
			mine = d.TaskGID == ""
		}
		if mine {
			r.Dropped, r.Sent = true, false
			return
		}
	}
}

// TaskGID is the target's first task gid (resolved), or "" when there is none.
func (r PushReport) TaskGID() string {
	if len(r.TaskGIDs) == 0 {
		return ""
	}
	return r.TaskGIDs[0]
}
