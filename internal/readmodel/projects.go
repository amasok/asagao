package readmodel

import "time"

type SectionView struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type ProjectView struct {
	GID          string        `json:"gid"`
	Name         string        `json:"name"`
	WorkspaceGID string        `json:"workspace_gid"`
	TeamName     string        `json:"team_name,omitempty"`
	IsMember     bool          `json:"is_member"`
	Archived     bool          `json:"archived"`
	PermalinkURL string        `json:"permalink_url"`
	Sections     []SectionView `json:"sections"`
	// Status is the type of the latest status update ("" if none).
	Status string `json:"status,omitempty"`
}

type StatusUpdateView struct {
	GID        string `json:"gid"`
	StatusType string `json:"status_type"`
	Title      string `json:"title"`
	Text       string `json:"text"`
	HTMLText   string `json:"html_text,omitempty"`
	Author     string `json:"author"`
	CreatedAt  string `json:"created_at"`
	Pending    bool   `json:"pending"`
}

// Stats summarises what is in the store (for `sync status`).
type Stats struct {
	Workspaces int        `json:"workspaces"`
	Projects   int        `json:"projects"`
	Sections   int        `json:"sections"`
	Tasks      int        `json:"tasks"`
	OpenTasks  int        `json:"open_tasks"`
	LastSync   *time.Time `json:"last_sync,omitempty"`
}
