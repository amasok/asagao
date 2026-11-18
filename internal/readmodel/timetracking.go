package readmodel

// TimeEntryView is a time tracking entry of a task (#6), as the entry points show it.
type TimeEntryView struct {
	GID             string `json:"gid"`
	TaskGID         string `json:"task_gid"`
	DurationMinutes int    `json:"duration_minutes"`
	// Duration is DurationMinutes written as "1h30m".
	Duration  string   `json:"duration"`
	EnteredOn string   `json:"entered_on"`
	Assignee  *UserRef `json:"assignee,omitempty"`
	CreatedBy *UserRef `json:"created_by,omitempty"`
	// Project is the project the time is counted for, if any.
	Project    string   `json:"project,omitempty"`
	Categories []string `json:"categories,omitempty"`
	// Pending is set while a change of the entry is still in the outbox.
	Pending bool `json:"pending"`
}

// TimeEntries are a task's time tracking entries in the local cache.
type TimeEntries struct {
	TaskGID string          `json:"task_gid"`
	Entries []TimeEntryView `json:"entries"`
	// TotalMinutes is the sum of the entries; Total is it written as "1h30m".
	TotalMinutes int    `json:"total_minutes"`
	Total        string `json:"total"`
	// FetchedAt is when the entries were last read from Asana; "" if never (the list then
	// holds only what was recorded here).
	FetchedAt string `json:"fetched_at,omitempty"`
}
