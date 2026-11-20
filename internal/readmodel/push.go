package readmodel

import "errors"

// PushResult is what one push of the outbox did (sync.Syncer.Push). It is here, not in
// internal/sync, so that the use case that sends a change right away (app.PushNow) can
// read it.
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
	Discarded []Discard `json:"discarded,omitempty"`
	// InboxReadOnly is set when Asana refused to mark notifications read / unread (no
	// notifications:write scope): those changes were dropped (see pushMarkNotification).
	InboxReadOnly bool `json:"inbox_read_only,omitempty"`
}

// ErrAccountChanged is returned by Push when the logged-in Asana account is not the one
// the local data belongs to: Asana answers 404 for everything, which must not be taken as
// deletions.
var ErrAccountChanged = errors.New("ログイン中の Asana アカウントが、ローカルのデータを同期したアカウントと違うため送れません")
