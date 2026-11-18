package readmodel

import (
	"math"
	"strconv"
	"strings"
)

// Goals, portfolios and time periods (#7), read from the local cache. They are fetched
// from Asana when shown (online) and need a plan that has them (PlanGoals, PlanPortfolios).

// GoalView is a goal.
type GoalView struct {
	GID   string `json:"gid"`
	Name  string `json:"name"`
	Notes string `json:"notes,omitempty"`
	// HTMLNotes is set once the goal itself was fetched (GoalDetail).
	HTMLNotes string `json:"html_notes,omitempty"`
	// Status: green, yellow, red (open) or missed, achieved, partial, dropped (closed); ""
	// when not set. GoalStatusLabel names it.
	Status         string          `json:"status,omitempty"`
	Owner          *UserRef        `json:"owner,omitempty"`
	TeamName       string          `json:"team_name,omitempty"`
	WorkspaceLevel bool            `json:"workspace_level,omitempty"`
	StartOn        string          `json:"start_on,omitempty"`
	DueOn          string          `json:"due_on,omitempty"`
	TimePeriod     *TimePeriodView `json:"time_period,omitempty"`
	Metric         *GoalMetric     `json:"metric,omitempty"`
}

// GoalDetail is a goal with what supports it.
type GoalDetail struct {
	GoalView
	Supporting []GoalSupport `json:"supporting"`
	// FetchedAt is when the goal itself was last fetched ("" never: only the list's fields
	// are known).
	FetchedAt string `json:"fetched_at,omitempty"`
}

// GoalMetric is how a goal's progress is measured. The values are nil when not set.
type GoalMetric struct {
	// Kind is number, percentage or currency.
	Kind     string   `json:"kind,omitempty"`
	Currency string   `json:"currency,omitempty"`
	Initial  *float64 `json:"initial,omitempty"`
	Target   *float64 `json:"target,omitempty"`
	Current  *float64 `json:"current,omitempty"`
	// Display is the current value as Asana shows it (e.g. "40%").
	Display string `json:"display,omitempty"`
	// Source is where the progress comes from: manual, subgoal_progress,
	// project_task_completion, project_milestone_completion, task_completion or external.
	Source    string `json:"source,omitempty"`
	Precision int    `json:"precision,omitempty"`
}

// Progress returns how far the current value is from the initial value toward the target,
// between 0 and 1 (ok is false when it cannot be told).
func (m *GoalMetric) Progress() (p float64, ok bool) {
	if m == nil || m.Target == nil || m.Current == nil {
		return 0, false
	}
	start := 0.0
	if m.Initial != nil {
		start = *m.Initial
	}
	span := *m.Target - start
	if span == 0 || math.IsNaN(span) || math.IsInf(span, 0) {
		return 0, false
	}
	p = (*m.Current - start) / span
	if math.IsNaN(p) {
		return 0, false
	}
	return math.Max(0, math.Min(1, p)), true
}

// Text describes the metric's progress, e.g. "40 / 100（40%）".
func (m *GoalMetric) Text() string {
	if m == nil {
		return ""
	}
	num := func(v *float64) string {
		if v == nil {
			return ""
		}
		s := strconv.FormatFloat(*v, 'f', max(0, min(m.Precision, 6)), 64)
		switch m.Kind {
		case "percentage":
			return s + "%"
		case "currency":
			return strings.TrimSpace(m.Currency + " " + s)
		}
		return s
	}
	cur := m.Display
	if cur == "" {
		cur = num(m.Current)
	}
	text := cur
	if t := num(m.Target); t != "" {
		text = strings.TrimSpace(cur + " / " + t)
	}
	if p, ok := m.Progress(); ok {
		text += "（" + strconv.Itoa(int(math.Floor(p*100))) + "%）"
	}
	return text
}

// GoalSupport is what supports a goal.
type GoalSupport struct {
	GID string `json:"gid"`
	// Type is goal (a subgoal), project, portfolio or task.
	Type   string   `json:"type"`
	Name   string   `json:"name"`
	Weight *float64 `json:"weight,omitempty"`
}

// TimePeriodView is a period goals are set for.
type TimePeriodView struct {
	GID         string `json:"gid"`
	DisplayName string `json:"display_name"`
	// Period is FY, H1, H2, Q1, Q2, Q3 or Q4.
	Period  string `json:"period,omitempty"`
	StartOn string `json:"start_on,omitempty"`
	EndOn   string `json:"end_on,omitempty"`
}

// Contains reports whether the period includes day (YYYY-MM-DD).
func (p TimePeriodView) Contains(day string) bool {
	return p.StartOn != "" && p.EndOn != "" && p.StartOn <= day && day <= p.EndOn
}

// GoalStatusLabel names a goal's status in Japanese.
func GoalStatusLabel(status string) string {
	switch status {
	case "green":
		return "順調"
	case "yellow":
		return "要注意"
	case "red":
		return "遅れ"
	case "achieved":
		return "達成"
	case "partial":
		return "一部達成"
	case "missed":
		return "未達"
	case "dropped":
		return "中止"
	case "":
		return "未設定"
	}
	return status
}

// PortfolioView is a portfolio.
type PortfolioView struct {
	GID          string       `json:"gid"`
	Name         string       `json:"name"`
	Color        string       `json:"color,omitempty"`
	Owner        *UserRef     `json:"owner,omitempty"`
	StartOn      string       `json:"start_on,omitempty"`
	DueOn        string       `json:"due_on,omitempty"`
	PermalinkURL string       `json:"permalink_url,omitempty"`
	Status       *StatusBrief `json:"latest_status,omitempty"`
}

// PortfolioDetail is a portfolio with its items.
type PortfolioDetail struct {
	PortfolioView
	Items []PortfolioItem `json:"items"`
	// FetchedAt is when the items were last fetched ("" never).
	FetchedAt string `json:"fetched_at,omitempty"`
}

// PortfolioItem is a project or a portfolio in a portfolio.
type PortfolioItem struct {
	GID string `json:"gid"`
	// Type is project or portfolio.
	Type         string       `json:"type"`
	Name         string       `json:"name"`
	Color        string       `json:"color,omitempty"`
	Archived     bool         `json:"archived,omitempty"`
	Owner        *UserRef     `json:"owner,omitempty"`
	StartOn      string       `json:"start_on,omitempty"`
	DueOn        string       `json:"due_on,omitempty"`
	PermalinkURL string       `json:"permalink_url,omitempty"`
	Status       *StatusBrief `json:"latest_status,omitempty"`
}

// StatusBrief is the latest status update of a project or a portfolio.
type StatusBrief struct {
	// StatusType is on_track, at_risk, off_track, on_hold or complete (StatusLabel).
	StatusType string `json:"status_type"`
	Title      string `json:"title,omitempty"`
}

// GoalList is the goals that a list shows, with the periods it can be narrowed to.
type GoalList struct {
	Goals []GoalView `json:"goals"`
	// Period is the period the list is narrowed to: its goals are those set for it or for a
	// period within it (nil: all goals, or those of the periods that include today).
	Period  *TimePeriodView  `json:"period,omitempty"`
	Periods []TimePeriodView `json:"periods"`
	// FetchedAt is when the goals were last fetched from Asana ("" never).
	FetchedAt string `json:"fetched_at,omitempty"`
}
