package model

import "time"

// Notification is an item of the user's Asana inbox (GET /users/{gid}/notifications).
// Scopes: notifications:read to list, notifications:write to mark read / unread.
type Notification struct {
	GID                  string    `json:"gid"`
	IsRead               bool      `json:"is_read"`
	CreatedAt            time.Time `json:"created_at"`
	CreatedBy            *Ref      `json:"created_by"`
	StoryResourceSubtype string    `json:"story_resource_subtype"`
	Story                *Story    `json:"story"`
	WorkObjects          []WorkRef `json:"work_objects"`
}

// WorkRef is a compact reference to what a notification is about (a task, project, ...).
type WorkRef struct {
	GID          string `json:"gid"`
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
}
