package readmodel

import "github.com/amasok/asagao/internal/domain/outbox"

// Discard is a local change dropped because its target was deleted in Asana, or because
// the user reset the local data (IsReset).
type Discard struct {
	ID       int64       `json:"id"`
	Kind     outbox.Kind `json:"kind"`
	TaskGID  string      `json:"task_gid,omitempty"`
	TaskName string      `json:"task_name,omitempty"`
	Summary  string      `json:"summary"` // what the change was, e.g. 「名前を「…」に変更」
	// Reason is why it was dropped: "reset" (DiscardReasonReset) for an unsent change
	// deleted by a reset, or else a message saying what Asana answered (e.g. that the task
	// was deleted there). The JSON output of the CLI and the MCP tools carries it as is.
	Reason      string `json:"reason,omitempty"`
	DiscardedAt string `json:"discarded_at"`
	// Payload is the dropped change as it was queued (e.g. the full comment text, the new
	// notes, the path of the attachment's local copy, which is kept), to recover it.
	Payload map[string]any `json:"payload,omitempty"`
}

// DiscardReasonReset is the Reason of an unsent change deleted by a reset (which the user
// confirmed). It is stored in discarded_changes.reason, so it must not change; it is a
// code, not text to show, so that rewording the display does not lose it (Issue #82; the
// store's migration turned the Japanese text recorded before into it).
const DiscardReasonReset = "reset"

// IsReset reports whether d was an unsent change deleted by a reset, rather than a change
// to something deleted in Asana.
func (d Discard) IsReset() bool { return d.Reason == DiscardReasonReset }
