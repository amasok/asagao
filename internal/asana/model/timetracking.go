package model

import "time"

// TimeEntry is a time tracking entry of a task (#6). Asana's lists give the compact form:
// CreatedAt is set only when one entry is read (GET /time_tracking_entries/{gid}) or
// returned by a create or an update.
type TimeEntry struct {
	GID             string `json:"gid"`
	DurationMinutes int    `json:"duration_minutes"`
	// EnteredOn is the day the time is recorded for ("YYYY-MM-DD").
	EnteredOn string `json:"entered_on"`
	// Assignee is the user the time is attributed to (the creator unless set otherwise).
	Assignee  *Ref      `json:"assignee,omitempty"`
	CreatedBy *Ref      `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// AttributableTo is the project the time is counted for, if any.
	AttributableTo *Ref  `json:"attributable_to,omitempty"`
	Categories     []Ref `json:"categories,omitempty"`
}
