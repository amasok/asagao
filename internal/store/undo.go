package store

import (
	"context"
	"database/sql"
	"fmt"
)

// undoFunc undoes the local change of an outbox entry that is discarded (DiscardOp,
// discardOpTx); the entry itself is deleted afterwards.
type undoFunc func(ctx context.Context, tx *sql.Tx, op *Op) error

// undoers holds the undo function of every outbox kind (TestEveryKindHasAnUndo). Each
// file that queues a domain's entries registers its own.
var undoers = map[Kind]undoFunc{}

// registerUndo sets the undo function of kind k. It is called from init functions: a
// kind registered twice, unknown to domain/outbox or without a function is a programming
// error (a kind with nothing to undo registers nothingToUndo).
func registerUndo(k Kind, f undoFunc) {
	if !k.Known() {
		panic(fmt.Sprintf("store: undo for unknown outbox kind %q", k))
	}
	if _, dup := undoers[k]; dup {
		panic(fmt.Sprintf("store: two undo functions for outbox kind %q", k))
	}
	if f == nil {
		panic(fmt.Sprintf("store: nil undo function for outbox kind %q", k))
	}
	undoers[k] = f
}

// nothingToUndo is the undo function of a kind whose local change goes with its entry.
func nothingToUndo(context.Context, *sql.Tx, *Op) error { return nil }

// undoWith adapts an apply function (op, undo) to an undo function.
func undoWith(apply func(ctx context.Context, tx *sql.Tx, op Op, undo bool) error) undoFunc {
	return func(ctx context.Context, tx *sql.Tx, op *Op) error { return apply(ctx, tx, *op, true) }
}
