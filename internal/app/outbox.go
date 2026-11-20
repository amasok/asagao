package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/ui/format"
)

// Resolving outbox entries (changes that could not be sent) is shared by the TUI, the CLI
// and the MCP server. Resolve does the local part; the caller then does the network part
// it asks for (push the requeued entry, refresh the task), each with its own plumbing.

// OutboxStore is the part of the local cache that resolving outbox entries needs.
// *store.Store satisfies it.
type OutboxStore interface {
	GetOp(ctx context.Context, id int64) (*outbox.Op, error)
	ResolveKeepLocal(ctx context.Context, id int64) error
	RetryOp(ctx context.Context, id int64) error
	DiscardOp(ctx context.Context, id int64) error
	PendingDiscards(ctx context.Context) ([]readmodel.Discard, error)
	MarkDiscardsNotified(ctx context.Context, ids []int64) error
}

// Outbox is the use case on outbox entries: resolving one.
type Outbox struct {
	Store OutboxStore
}

// ResolveAction says what to do with an outbox entry.
type ResolveAction string

const (
	// KeepLocal overwrites Asana with the local value (conflicts only).
	KeepLocal ResolveAction = "keep_local"
	// KeepRemote drops the local change in favour of Asana's value (conflicts only).
	KeepRemote ResolveAction = "keep_remote"
	// Retry sends a failed entry again (failures only).
	Retry ResolveAction = "retry"
	// Discard drops the local change, whatever its state.
	Discard ResolveAction = "discard"
)

// ErrWrongState matches (errors.Is) a StateError.
var ErrWrongState = errors.New("action does not apply to the outbox entry's state")

// StateError is returned when an action does not apply to the entry's state: keep_local
// and keep_remote need a conflict, retry needs a failure.
type StateError struct {
	ID     int64
	State  string
	Action ResolveAction
}

func (e *StateError) Error() string {
	want := "競合している変更"
	if e.Action == Retry {
		want = "送信に失敗した変更"
	}
	return fmt.Sprintf("#%d は%sではありません（状態: %s）。%s は%sにだけ使えます", e.ID, want, format.OpStateLabel(e.State), e.Action, want)
}

func (e *StateError) Is(target error) bool { return target == ErrWrongState }

// ParseResolveAction checks an action name.
func ParseResolveAction(s string) (ResolveAction, error) {
	switch a := ResolveAction(s); a {
	case KeepLocal, KeepRemote, Retry, Discard:
		return a, nil
	}
	return "", fmt.Errorf("action must be keep_local, keep_remote, retry or discard (got %q)", s)
}

// Resolution is what Resolve did and what the caller should do next.
type Resolution struct {
	Op     outbox.Op // the entry as it was before resolving
	Action ResolveAction
	// Push is true when the entry is back in the queue and should be sent now.
	Push bool
	// RefreshGID is the task whose Asana values should be fetched again after a local
	// change was dropped ("" when there is nothing to fetch: the entry created the task,
	// the task only exists locally, or the entry is about a section or project).
	RefreshGID string
}

// Resolve applies action to outbox entry id in the local store.
func (o *Outbox) Resolve(ctx context.Context, id int64, action ResolveAction) (Resolution, error) {
	op, err := o.Store.GetOp(ctx, id)
	if err != nil {
		return Resolution{}, err
	}
	r := Resolution{Op: *op, Action: action}
	if op.State == outbox.StateSending { // Push has it now; the store would refuse too
		return r, fmt.Errorf("#%d: %w", op.ID, readmodel.ErrOpSending)
	}
	switch action {
	case KeepLocal:
		if op.State != outbox.StateConflict {
			return r, &StateError{op.ID, op.State, action}
		}
		r.Push = true
		return r, o.Store.ResolveKeepLocal(ctx, op.ID)
	case Retry:
		if op.State != outbox.StateFailed {
			return r, &StateError{op.ID, op.State, action}
		}
		r.Push = true
		return r, o.Store.RetryOp(ctx, op.ID)
	case KeepRemote, Discard:
		if action == KeepRemote && op.State != outbox.StateConflict {
			return r, &StateError{op.ID, op.State, action}
		}
		if err := o.Store.DiscardOp(ctx, op.ID); err != nil {
			return r, err
		}
		r.RefreshGID = refreshTarget(*op)
		return r, nil
	}
	_, err = ParseResolveAction(string(action))
	return r, err
}

// refreshTarget is the task to re-fetch after dropping op, or "".
func refreshTarget(op outbox.Op) string {
	if gid.IsLocal(op.TaskGID) {
		return "" // removed by DiscardOp, or never reached Asana
	}
	if !op.Kind.OnTask() {
		// A create's local task is gone; task_gid of the others is a section, a project, a
		// tag, a notification or an option.
		return ""
	}
	return op.TaskGID
}

// MarkDiscardsNotified marks the dropped changes ids as announced (a sync's own, which it
// reports itself).
func (o *Outbox) MarkDiscardsNotified(ctx context.Context, ids []int64) error {
	return o.Store.MarkDiscardsNotified(ctx, ids)
}

// TakenDiscards are the dropped changes not announced yet, taken by TakeDiscards.
type TakenDiscards struct {
	// All are every one of them; Announce those to tell the user about (a change deleted
	// by a reset was confirmed by the user, and is not).
	All, Announce []readmodel.Discard
	// MarkErr is why they could not be marked as announced: they come again next time.
	MarkErr error
}

// TakeDiscards reads the dropped changes not announced yet and marks them as announced
// (the TUI and the CLI each announce them once). When none are, nothing is marked.
func (o *Outbox) TakeDiscards(ctx context.Context) (TakenDiscards, error) {
	var t TakenDiscards
	all, err := o.Store.PendingDiscards(ctx)
	if err != nil || len(all) == 0 {
		return t, err
	}
	t.All = all
	ids := make([]int64, len(all))
	for i, d := range all {
		ids[i] = d.ID
		if !d.IsReset() {
			t.Announce = append(t.Announce, d)
		}
	}
	t.MarkErr = o.Store.MarkDiscardsNotified(ctx, ids)
	return t, nil
}
