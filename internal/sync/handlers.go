package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

// opHandler is what Push does with the entries of one outbox kind. Every kind has one
// (TestEveryKindHasAHandler); each file that sends a domain's entries registers its own
// (push_tasks.go, push_relations.go, push_comments.go, push_sections.go, push_tags.go,
// push_fields.go, projects.go, brief.go, timetracking.go, inbox.go, my_sections.go) as a
// handlerOf its kind's payload struct (see registerOf). Only send is required.
//
// An entry whose payload cannot be read as its kind's struct, or of a kind this version
// does not know, is never handed to a handler: Push holds it (holdUnreadable).
type opHandler struct {
	send          func(s *Pusher, ctx context.Context, op outbox.Op) (outcome, error)
	sendAlong     func(s *Pusher, ctx context.Context, op outbox.Op) (out outcome, alsoSent int, err error)
	startOn       func(op outbox.Op) (start string, ok bool)
	batch         func(s *Pusher, ctx context.Context, op outbox.Op, e *batchEntry) (ok bool, err error)
	find          func(s *Pusher, ctx context.Context, op outbox.Op, f finder) (outcome, match, error)
	settle        time.Duration
	local         func(op outbox.Op) (gid string, result readmodel.ResultKind, ok bool)
	fieldValues   func(op outbox.Op) map[string]any
	waits         func(s *Pusher, ctx context.Context, op outbox.Op) (bool, error)
	forbidden     func(s *Pusher, ctx context.Context, op outbox.Op, cause error) error
	goneReason    func(op outbox.Op, cause error) string
	planWorkspace func(s *Pusher, ctx context.Context, op outbox.Op) string
}

// handlerOf is the handler of a kind whose payload struct is P: each function gets the
// entry's payload as P besides the entry.
type handlerOf[P any] struct {
	// send sends the entry (already marked "sending").
	send func(s *Pusher, ctx context.Context, op outbox.Op, p P) (outcome, error)
	// sendAlong, when set, is used by Push instead of send: it sends the entry together
	// with others of its kind queued after it, and returns how many of those it sent.
	sendAlong func(s *Pusher, ctx context.Context, op outbox.Op, p P) (out outcome, alsoSent int, err error)
	// startOn returns the start date the payload sets or clears (nil: none), which some
	// plans lack (see startOnOutcome).
	startOn func(p P) *string
	// batch fills in e for an entry that can go out in a POST /batch (ok); nil: always
	// sent on its own. e.task is the entry's task, an Asana gid.
	batch func(s *Pusher, ctx context.Context, op outbox.Op, p P, e *batchEntry) (ok bool, err error)
	// find looks in Asana for the result of an entry that may have been applied (see
	// reconcile) and records it like send would; nil: there is nothing to look for (the
	// entry is harmless to send again).
	find func(s *Pusher, ctx context.Context, op outbox.Op, p P, f finder) (outcome, match, error)
	// settle is how long after sending the entry its result may still be missing from
	// Asana's lists (0: settleDefault).
	settle time.Duration
	// local is, for an entry that edits a comment, an attachment or a time entry, that
	// item's gid in the payload, and the ResultKind its create records as the result: an
	// item not created in Asana yet makes the entry wait.
	local func(p P) string
	// localResult is the ResultKind of local.
	localResult readmodel.ResultKind
	// fieldValues returns the custom field values the entry sets (field gid → value):
	// an option or a field of the project still being created makes it wait.
	fieldValues func(p P) map[string]any
	// waits is any other reason for the entry to wait (see waitsForCreate).
	waits func(s *Pusher, ctx context.Context, op outbox.Op, p P) (bool, error)
	// forbidden turns a 403 to the entry into the error recorded for it (and may record
	// that the user cannot make such a change); nil: the 403 as it is.
	forbidden func(s *Pusher, ctx context.Context, op outbox.Op, p P, cause error) error
	// goneReason says why the entry is dropped when Asana answers 404 although the task
	// is there (something else it names is missing); nil or "": goneReasonDefault.
	goneReason func(op outbox.Op, p P, cause error) string
	// planWorkspace returns the workspace a 402 to the entry is recorded for ("": the
	// default workspace), as the app reads it before queuing one (app.planIn).
	planWorkspace func(s *Pusher, ctx context.Context, op outbox.Op, p P) string
}

// settleDefault is how long after sending an entry its result may still be missing from
// Asana's lists, unless its handler says otherwise.
const settleDefault = time.Minute

// handlers holds the handler of every outbox kind (see register).
var handlers = map[outbox.Kind]opHandler{}

// registerOf sets the handler of kind k, whose payload struct is P. It is called from
// init functions: a kind registered twice, unknown to domain/outbox, of another payload
// struct or without send is a programming error.
func registerOf[P any](k outbox.Kind, h handlerOf[P]) {
	if !k.Known() {
		panic(fmt.Sprintf("sync: handler for unknown outbox kind %q", k))
	}
	outbox.CheckPayloadType[P](k)
	if _, dup := handlers[k]; dup {
		panic(fmt.Sprintf("sync: two handlers for outbox kind %q", k))
	}
	if h.send == nil {
		panic(fmt.Sprintf("sync: handler for outbox kind %q cannot send", k))
	}
	handlers[k] = h.untyped()
}

// untyped returns h as an opHandler: each function reads the entry's payload as P first.
// Push hands handlers only entries whose payload reads (holdUnreadable), so a payload
// that does not read here is an error to send, and nothing for the rest.
func (h handlerOf[P]) untyped() opHandler {
	payload := outbox.PayloadOf[P]
	o := opHandler{settle: h.settle}
	o.send = func(s *Pusher, ctx context.Context, op outbox.Op) (outcome, error) {
		p, err := payload(op)
		if err != nil {
			return sent, &unreadableError{err: err}
		}
		return h.send(s, ctx, op, p)
	}
	if h.sendAlong != nil {
		o.sendAlong = func(s *Pusher, ctx context.Context, op outbox.Op) (outcome, int, error) {
			p, err := payload(op)
			if err != nil {
				return sent, 0, &unreadableError{err: err}
			}
			return h.sendAlong(s, ctx, op, p)
		}
	}
	if h.startOn != nil {
		o.startOn = func(op outbox.Op) (string, bool) {
			p, err := payload(op)
			if err != nil {
				return "", false
			}
			v := h.startOn(p)
			return outbox.Str(v), v != nil
		}
	}
	if h.batch != nil {
		o.batch = func(s *Pusher, ctx context.Context, op outbox.Op, e *batchEntry) (bool, error) {
			p, err := payload(op)
			if err != nil {
				return false, nil // sent on its own: held there
			}
			return h.batch(s, ctx, op, p, e)
		}
	}
	if h.find != nil {
		o.find = func(s *Pusher, ctx context.Context, op outbox.Op, f finder) (outcome, match, error) {
			p, err := payload(op)
			if err != nil {
				return sent, notFound, &unreadableError{err: err}
			}
			return h.find(s, ctx, op, p, f)
		}
	}
	if h.local != nil {
		o.local = func(op outbox.Op) (string, readmodel.ResultKind, bool) {
			p, err := payload(op)
			if err != nil {
				return "", h.localResult, false
			}
			return h.local(p), h.localResult, true
		}
	}
	if h.fieldValues != nil {
		o.fieldValues = func(op outbox.Op) map[string]any {
			p, err := payload(op)
			if err != nil {
				return nil
			}
			return h.fieldValues(p)
		}
	}
	if h.waits != nil {
		o.waits = func(s *Pusher, ctx context.Context, op outbox.Op) (bool, error) {
			p, err := payload(op)
			if err != nil {
				return false, nil
			}
			return h.waits(s, ctx, op, p)
		}
	}
	if h.forbidden != nil {
		o.forbidden = func(s *Pusher, ctx context.Context, op outbox.Op, cause error) error {
			p, err := payload(op)
			if err != nil {
				return cause
			}
			return h.forbidden(s, ctx, op, p, cause)
		}
	}
	if h.goneReason != nil {
		o.goneReason = func(op outbox.Op, cause error) string {
			p, err := payload(op)
			if err != nil {
				return ""
			}
			return h.goneReason(op, p, cause)
		}
	}
	if h.planWorkspace != nil {
		o.planWorkspace = func(s *Pusher, ctx context.Context, op outbox.Op) string {
			p, err := payload(op)
			if err != nil {
				return ""
			}
			return h.planWorkspace(s, ctx, op, p)
		}
	}
	return o
}

// sends adapts a send function that has no outcome to report.
func sends[P any](f func(s *Pusher, ctx context.Context, op outbox.Op, p P) error) func(*Pusher, context.Context, outbox.Op, P) (outcome, error) {
	return func(s *Pusher, ctx context.Context, op outbox.Op, p P) (outcome, error) {
		return sent, f(s, ctx, op, p)
	}
}

// finder is what find functions need besides the entry: since when to look, and who the
// user is.
type finder struct {
	since time.Time // results created before are not the entry's
	me    string    // the user's gid; "" before the first sync: not filtered
}

// byMe reports whether something Asana says r created was created by the user (or Asana
// does not say).
func (f finder) byMe(r *model.Ref) bool { return r == nil || f.me == "" || r.GID == f.me }

// recent reports whether something created at t may be the entry's result.
func (f finder) recent(t time.Time) bool { return !t.Before(f.since) }

// taken returns the Asana gids already recorded as the results of other entries.
func (s *Pusher) taken(ctx context.Context, kind readmodel.ResultKind) (map[string]bool, error) {
	return s.Store.RecordedResults(ctx, kind)
}
