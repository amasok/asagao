package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// undoFunc undoes the local change of an outbox entry that is discarded (DiscardOp,
// discardOpTx); the entry itself is deleted afterwards.
type undoFunc func(ctx context.Context, tx *sql.Tx, op *Op) error

// undoers holds the undo function of every outbox kind (TestEveryKindHasAnUndo). Each
// file that queues a domain's entries registers its own.
var undoers = map[Kind]undoFunc{}

// registerUndo sets the undo function of kind k, whose payload struct is P. It is called
// from init functions: a kind registered twice, unknown to domain/outbox, of another
// payload struct or without a function is a programming error (a kind with nothing to
// undo registers nothingToUndo).
//
// f gets the payload as far as it reads (outbox.BestEffortPayload): an entry this version
// cannot send (a key or a type of a newer version) is still undone as far as it can be
// when it is discarded, as it was before payloads were structs.
func registerUndo[P any](k Kind, f func(ctx context.Context, tx *sql.Tx, op *Op, p P) error) {
	if !k.Known() {
		panic(fmt.Sprintf("store: undo for unknown outbox kind %q", k))
	}
	outbox.CheckPayloadType[P](k)
	if _, dup := undoers[k]; dup {
		panic(fmt.Sprintf("store: two undo functions for outbox kind %q", k))
	}
	if f == nil {
		panic(fmt.Sprintf("store: nil undo function for outbox kind %q", k))
	}
	undoers[k] = func(ctx context.Context, tx *sql.Tx, op *Op) error {
		p, _ := outbox.BestEffortPayload[P](*op)
		return f(ctx, tx, op, p)
	}
}

// nothingToUndo is the undo function of a kind whose local change goes with its entry.
func nothingToUndo[P any](context.Context, *sql.Tx, *Op, P) error { return nil }

// undoWith adapts an apply function (op, undo), which reads the payload itself, to an
// undo function.
func undoWith[P any](apply func(ctx context.Context, tx *sql.Tx, op Op, undo bool) error) func(context.Context, *sql.Tx, *Op, P) error {
	return func(ctx context.Context, tx *sql.Tx, op *Op, _ P) error { return apply(ctx, tx, *op, true) }
}
