package sync

import (
	"context"
	"errors"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// An entry this version cannot send is held, not failed: one of a kind it does not know
// (written by a newer version) or whose payload does not read as its kind's struct (a key
// or a type of a newer version, or damaged). Asana is not asked anything for it: it stays
// in the queue as it is, with a note saying why, and is looked at again on every push, so
// that the version that wrote it (or a newer one) sends it. The user may discard it.

// unreadableError says that an entry was not sent because its payload does not read.
type unreadableError struct{ err error }

func (e *unreadableError) Error() string { return heldNote(e.err) }
func (e *unreadableError) Unwrap() error { return e.err }

// heldNote is what an entry held for err (outbox.Readable) shows as its last error.
func heldNote(err error) string {
	if errors.Is(err, outbox.ErrUnknownKind) {
		return "この版の asagao では送れない種類の変更です。新しい版で送れるよう、そのまま残しています"
	}
	return "変更の内容を読めないため、送らずに残しています (新しい版の asagao なら送れるかもしれません): " + err.Error()
}

// holdUnreadable holds op (pending, not sent yet in this push) if this version cannot
// send it. held: it is not handled now.
func (s *Pusher) holdUnreadable(ctx context.Context, op outbox.Op) (held bool, err error) {
	rerr := outbox.Readable(op)
	if rerr == nil {
		return false, nil
	}
	if note := heldNote(rerr); op.LastError != note {
		return true, s.Store.HoldOp(ctx, op.ID, note)
	}
	return true, nil
}

// holdBackUnreadable puts op, marked "sending", back in the queue unsent and held (an
// unreadable payload found while sending it: holdUnreadable checks before).
func (s *Pusher) holdBackUnreadable(ctx context.Context, op outbox.Op, cause error) error {
	ctx = context.WithoutCancel(ctx)
	if err := s.Store.ReleaseNotSent(ctx, op.ID); err != nil {
		return err
	}
	var u *unreadableError
	errors.As(cause, &u)
	return s.Store.HoldOp(ctx, op.ID, heldNote(u.err))
}
