package readmodel

import "errors"

// ErrNotFound is returned when a task or project is not in the local store.
var ErrNotFound = errors.New("not found in local store")

// ErrAmbiguous is returned when a project name matches more than one project.
var ErrAmbiguous = errors.New("ambiguous name")

// ErrPushBusy is returned by TryLockPush when another process (or goroutine) is sending
// the outbox.
var ErrPushBusy = errors.New("別のプロセスが未送信の変更を送信中です")

// ErrUnsentChanges is returned by Reset (without force) when local changes have not been
// sent to Asana yet; Reset then deletes nothing.
var ErrUnsentChanges = errors.New("Asana に送っていない変更があります")

// PlanUnavailableError is returned for a feature that Asana refused because the
// workspace's plan lacks it (see KeyPlanUnavailable). The message says which.
type PlanUnavailableError struct{ Feature string }

func (e *PlanUnavailableError) Error() string { return PlanUnavailableMessage(e.Feature) }
