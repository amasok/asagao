package readmodel

// Discard is a local change dropped because its target was deleted in Asana.
type Discard struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	TaskGID     string `json:"task_gid,omitempty"`
	TaskName    string `json:"task_name,omitempty"`
	Summary     string `json:"summary"` // what the change was, e.g. 「名前を「…」に変更」
	Reason      string `json:"reason,omitempty"`
	DiscardedAt string `json:"discarded_at"`
	// Payload is the dropped change as it was queued (e.g. the full comment text, the new
	// notes, the path of the attachment's local copy, which is kept), to recover it.
	Payload map[string]any `json:"payload,omitempty"`
}
