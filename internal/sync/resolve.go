package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/amasok/asagao/internal/store"
)

// Resolving outbox entries (changes that could not be sent) is shared by the TUI, the CLI
// and the MCP server. Resolve does the local part; the caller then does the network part
// it asks for (push the requeued entry, refresh the task), each with its own plumbing.

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
	return fmt.Sprintf("#%d は%sではありません（状態: %s）。%s は%sにだけ使えます", e.ID, want, store.OpStateLabel(e.State), e.Action, want)
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
	Op     store.Op // the entry as it was before resolving
	Action ResolveAction
	// Push is true when the entry is back in the queue and should be sent now.
	Push bool
	// RefreshGID is the task whose Asana values should be fetched again after a local
	// change was dropped ("" when there is nothing to fetch: the entry created the task,
	// the task only exists locally, or the entry is about a section or project).
	RefreshGID string
}

// Resolve applies action to outbox entry id in the local store.
func Resolve(ctx context.Context, st *store.Store, id int64, action ResolveAction) (Resolution, error) {
	op, err := st.GetOp(ctx, id)
	if err != nil {
		return Resolution{}, err
	}
	r := Resolution{Op: *op, Action: action}
	if op.State == store.OpSending { // Push has it now; the store would refuse too
		return r, fmt.Errorf("#%d: %w", op.ID, store.ErrOpSending)
	}
	switch action {
	case KeepLocal:
		if op.State != store.OpConflict {
			return r, &StateError{op.ID, op.State, action}
		}
		r.Push = true
		return r, st.ResolveKeepLocal(ctx, op.ID)
	case Retry:
		if op.State != store.OpFailed {
			return r, &StateError{op.ID, op.State, action}
		}
		r.Push = true
		return r, st.RetryOp(ctx, op.ID)
	case KeepRemote, Discard:
		if action == KeepRemote && op.State != store.OpConflict {
			return r, &StateError{op.ID, op.State, action}
		}
		if err := st.DiscardOp(ctx, op.ID); err != nil {
			return r, err
		}
		r.RefreshGID = refreshTarget(*op)
		return r, nil
	}
	_, err = ParseResolveAction(string(action))
	return r, err
}

// refreshTarget is the task to re-fetch after dropping op, or "".
func refreshTarget(op store.Op) string {
	if store.IsLocalGID(op.TaskGID) {
		return "" // removed by DiscardOp, or never reached Asana
	}
	switch op.Kind {
	case store.OpCreateTask, store.OpDuplicateTask, // the task was local and is gone
		store.OpRenameSection, store.OpCreateStatus, // task_gid is a section / project
		store.OpMarkNotification: // task_gid is a notification
		return ""
	}
	return op.TaskGID
}
