package readmodel

import "time"

type SectionView struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

type ProjectView struct {
	GID          string `json:"gid"`
	Name         string `json:"name"`
	WorkspaceGID string `json:"workspace_gid"`
	TeamGID      string `json:"team_gid,omitempty"`
	TeamName     string `json:"team_name,omitempty"`
	// Favorite: the user marked the project a favorite in Asana. ListProjects lists the
	// favorites first (in the user's order), then by team.
	Favorite     bool          `json:"favorite,omitempty"`
	IsMember     bool          `json:"is_member"`
	Archived     bool          `json:"archived"`
	PermalinkURL string        `json:"permalink_url"`
	Sections     []SectionView `json:"sections"`
	// Status is the type of the latest status update ("" if none).
	Status string `json:"status,omitempty"`
}

// TeamView is a team (of the teams I am in, or of a cached project).
type TeamView struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
}

// ProjectOverview is what the project overview shows: the project's fields, its
// progress and brief (fetched when the overview is opened online) and its latest status.
type ProjectOverview struct {
	GID          string   `json:"gid"`
	Name         string   `json:"name"`
	TeamName     string   `json:"team_name,omitempty"`
	Color        string   `json:"color,omitempty"`
	Notes        string   `json:"notes"`
	HTMLNotes    string   `json:"html_notes,omitempty"`
	Owner        *UserRef `json:"owner,omitempty"`
	StartOn      string   `json:"start_on,omitempty"`
	DueOn        string   `json:"due_on,omitempty"`
	PermalinkURL string   `json:"permalink_url"`
	Archived     bool     `json:"archived"`
	Favorite     bool     `json:"favorite,omitempty"`
	// Pending: changes to the fields are not sent yet.
	Pending bool `json:"pending,omitempty"`
	// Counts is nil until the overview was fetched from Asana.
	Counts *TaskCounts `json:"counts,omitempty"`
	// Brief is nil when the project has none, it was not fetched yet, or Asana does not
	// give it (the API is a preview and may need another plan).
	Brief  *ProjectBrief     `json:"brief,omitempty"`
	Status *StatusUpdateView `json:"latest_status,omitempty"`
	// FetchedAt is when the counts and the brief were fetched ("" never).
	FetchedAt string `json:"fetched_at,omitempty"`
}

// TaskCounts is how many tasks (and milestones) a project has.
type TaskCounts struct {
	Tasks               int `json:"tasks"`
	CompletedTasks      int `json:"completed_tasks"`
	Milestones          int `json:"milestones"`
	CompletedMilestones int `json:"completed_milestones"`
}

// ProjectBrief is a project's brief.
type ProjectBrief struct {
	Title    string `json:"title,omitempty"`
	Text     string `json:"text"`
	HTMLText string `json:"html_text,omitempty"`
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

// ProjectGroup is the heading a project is listed under (ListProjects' order): ★ お気に入り,
// its team, or チームなし.
func ProjectGroup(p ProjectView) string {
	switch {
	case p.Favorite:
		return "★ お気に入り"
	case p.TeamName != "":
		return p.TeamName
	case p.TeamGID != "":
		return p.TeamGID
	}
	return "チームなし"
}
