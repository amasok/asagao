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
// push_fields.go, projects.go, brief.go, timetracking.go, inbox.go, my_sections.go).
// Only send is required.
type opHandler struct {
	// send sends the entry (already marked "sending").
	send func(s *Syncer, ctx context.Context, op outbox.Op) (outcome, error)
	// sendAlong, when set, is used by Push instead of send: it sends the entry together
	// with others of its kind queued after it, and returns how many of those it sent.
	sendAlong func(s *Syncer, ctx context.Context, op outbox.Op) (out outcome, alsoSent int, err error)
	// startOn: the payload may set a task's start date, which some plans lack (see
	// startOnOutcome).
	startOn bool
	// batch fills in e for an entry that can go out in a POST /batch (ok); nil: always
	// sent on its own. e.task is the entry's task, an Asana gid.
	batch func(s *Syncer, ctx context.Context, op outbox.Op, e *batchEntry) (ok bool, err error)
	// find looks in Asana for the result of an entry that may have been applied (see
	// reconcile) and records it like send would; nil: there is nothing to look for (the
	// entry is harmless to send again).
	find func(s *Syncer, ctx context.Context, op outbox.Op, f finder) (outcome, match, error)
	// settle is how long after sending the entry its result may still be missing from
	// Asana's lists (0: settleDefault).
	settle time.Duration
	// local is, for an entry that edits a comment, an attachment or a time entry, where
	// it holds that item's gid and what its create records as the result: an item not
	// created in Asana yet makes the entry wait.
	local localRef
	// fieldValues returns the custom field values the entry sets (field gid → value):
	// an option or a field of the project still being created makes it wait.
	fieldValues func(op outbox.Op) map[string]any
	// waits is any other reason for the entry to wait (see waitsForCreate).
	waits func(s *Syncer, ctx context.Context, op outbox.Op) (bool, error)
	// forbidden turns a 403 to the entry into the error recorded for it (and may record
	// that the user cannot make such a change); nil: the 403 as it is.
	forbidden func(s *Syncer, ctx context.Context, op outbox.Op, cause error) error
	// goneReason says why the entry is dropped when Asana answers 404 although the task
	// is there (something else it names is missing); nil or "": goneReasonDefault.
	goneReason func(op outbox.Op, cause error) string
	// planWorkspace returns the workspace a 402 to the entry is recorded for ("": the
	// default workspace), as the app reads it before queuing one (app.planIn).
	planWorkspace func(s *Syncer, ctx context.Context, op outbox.Op) string
}

// localRef is where an entry holds the gid of a comment, attachment or time entry, and
// the ResultKind under which a local one's result is recorded.
type localRef struct {
	key    string
	result readmodel.ResultKind
}

// settleDefault is how long after sending an entry its result may still be missing from
// Asana's lists, unless its handler says otherwise.
const settleDefault = time.Minute

// handlers holds the handler of every outbox kind (see register).
var handlers = map[outbox.Kind]opHandler{}

// register sets the handler of kind k. It is called from init functions: a kind
// registered twice, unknown to domain/outbox or without send is a programming error.
func register(k outbox.Kind, h opHandler) {
	if !k.Known() {
		panic(fmt.Sprintf("sync: handler for unknown outbox kind %q", k))
	}
	if _, dup := handlers[k]; dup {
		panic(fmt.Sprintf("sync: two handlers for outbox kind %q", k))
	}
	if h.send == nil {
		panic(fmt.Sprintf("sync: handler for outbox kind %q cannot send", k))
	}
	handlers[k] = h
}

// sends adapts a send function that has no outcome to report.
func sends(f func(s *Syncer, ctx context.Context, op outbox.Op) error) func(*Syncer, context.Context, outbox.Op) (outcome, error) {
	return func(s *Syncer, ctx context.Context, op outbox.Op) (outcome, error) {
		return sent, f(s, ctx, op)
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
func (s *Syncer) taken(ctx context.Context, kind readmodel.ResultKind) (map[string]bool, error) {
	return s.Store.RecordedResults(ctx, kind)
}

// payloadString returns the string under key k of op's payload ("" if none).
func payloadString(op outbox.Op, k string) string {
	v, _ := op.Payload[k].(string)
	return v
}
