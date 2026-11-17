package model

import "time"

// Event is one change from GET /events.
type Event struct {
	Action   string `json:"action"` // changed, added, removed, deleted, undeleted
	Resource struct {
		GID          string `json:"gid"`
		ResourceType string `json:"resource_type"`
	} `json:"resource"`
	// Parent is set for added / removed (e.g. the task a subtask was added to).
	Parent *struct {
		GID          string `json:"gid"`
		ResourceType string `json:"resource_type"`
	} `json:"parent"`
	Change *struct {
		Field  string `json:"field"`
		Action string `json:"action"`
	} `json:"change"`
	CreatedAt time.Time `json:"created_at"`
}

// EventPage is one answer of GET /events: up to 100 events, the token for the next call,
// and whether more are waiting.
type EventPage struct {
	Events  []Event
	Sync    string
	HasMore bool
}
