package model

// Goals, portfolios and time periods (#7). They need a paid plan: GET /goals answers 402
// on a plan without goals; GET /portfolios may give an empty list.

// Goal is a goal (GET /goals, GET /goals/{gid}).
type Goal struct {
	GID   string `json:"gid"`
	Name  string `json:"name"`
	Notes string `json:"notes"`
	// HTMLNotes is only asked for by Client.Goal.
	HTMLNotes string `json:"html_notes,omitempty"`
	// Status is green, yellow or red while the goal is open, and missed, achieved, partial
	// or dropped once it is closed ("" when not set).
	Status           string      `json:"status"`
	Owner            *Ref        `json:"owner"`
	Team             *Ref        `json:"team"`
	Workspace        *Ref        `json:"workspace"`
	IsWorkspaceLevel bool        `json:"is_workspace_level"`
	StartOn          string      `json:"start_on"`
	DueOn            string      `json:"due_on"`
	TimePeriod       *TimePeriod `json:"time_period"`
	Metric           *GoalMetric `json:"metric"`
}

// GoalMetric is how a goal's progress is measured.
type GoalMetric struct {
	// ResourceSubtype is "number" on the real Asana for every metric (the reference also
	// names percentage and currency); the kind is in Unit.
	ResourceSubtype string `json:"resource_subtype"`
	// Unit is none, currency or percentage. A percentage's values are fractions: a target
	// of 1 is 100% (a new goal's default metric is percentage, 0 → 1).
	Unit                string   `json:"unit"`
	Precision           int      `json:"precision"`
	CurrencyCode        string   `json:"currency_code"`
	InitialNumberValue  *float64 `json:"initial_number_value"`
	TargetNumberValue   *float64 `json:"target_number_value"`
	CurrentNumberValue  *float64 `json:"current_number_value"`
	CurrentDisplayValue string   `json:"current_display_value"`
	// ProgressSource is manual, subgoal_progress, project_task_completion,
	// project_milestone_completion, task_completion or external.
	ProgressSource string `json:"progress_source"`
}

// GoalRelationship is what supports a goal: a subgoal, a project, a portfolio or a task
// (GET /goal_relationships?supported_goal=).
type GoalRelationship struct {
	GID string `json:"gid"`
	// ResourceSubtype is subgoal or supporting_work.
	ResourceSubtype    string          `json:"resource_subtype"`
	ContributionWeight *float64        `json:"contribution_weight"`
	SupportingResource *SupportingWork `json:"supporting_resource"`
}

// SupportingWork is the resource a goal relationship points to.
type SupportingWork struct {
	GID string `json:"gid"`
	// ResourceType is goal, project, portfolio or task.
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
}

// TimePeriod is a period goals are set for (GET /time_periods): a fiscal year (FY), a half
// (H1, H2) or a quarter (Q1 to Q4).
type TimePeriod struct {
	GID         string `json:"gid"`
	DisplayName string `json:"display_name"`
	Period      string `json:"period"`
	StartOn     string `json:"start_on"`
	EndOn       string `json:"end_on"`
	Parent      *Ref   `json:"parent"`
}

// Portfolio is a portfolio (GET /portfolios).
type Portfolio struct {
	GID                 string       `json:"gid"`
	Name                string       `json:"name"`
	Color               string       `json:"color"`
	Owner               *Ref         `json:"owner"`
	StartOn             string       `json:"start_on"`
	DueOn               string       `json:"due_on"`
	PermalinkURL        string       `json:"permalink_url"`
	CurrentStatusUpdate *StatusBrief `json:"current_status_update"`
}

// PortfolioItem is a project or a portfolio in a portfolio (GET /portfolios/{gid}/items).
type PortfolioItem struct {
	GID string `json:"gid"`
	// ResourceType is project or portfolio.
	ResourceType        string       `json:"resource_type"`
	Name                string       `json:"name"`
	Color               string       `json:"color"`
	Archived            bool         `json:"archived"`
	Owner               *Ref         `json:"owner"`
	StartOn             string       `json:"start_on"`
	DueOn               string       `json:"due_on"`
	PermalinkURL        string       `json:"permalink_url"`
	CurrentStatusUpdate *StatusBrief `json:"current_status_update"`
}

// StatusBrief is the latest status update of a project or a portfolio, as much of it as a
// list shows.
type StatusBrief struct {
	GID string `json:"gid"`
	// StatusType is on_track, at_risk, off_track, on_hold or complete.
	StatusType string `json:"status_type"`
	Title      string `json:"title"`
}
