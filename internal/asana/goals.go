package asana

import (
	"context"
	"net/url"
	"slices"

	"github.com/amasok/asagao/internal/asana/model"
)

// Goals, portfolios and time periods (#7). Read only.

var (
	timePeriodFields = []string{"display_name", "period", "start_on", "end_on", "parent.display_name"}
	goalFields       = []string{"name", "notes", "status", "owner.name", "team.name", "workspace", "is_workspace_level",
		"start_on", "due_on", "time_period.display_name", "time_period.period", "time_period.start_on", "time_period.end_on",
		"metric.resource_subtype", "metric.unit", "metric.precision", "metric.currency_code", "metric.initial_number_value",
		"metric.target_number_value", "metric.current_number_value", "metric.current_display_value", "metric.progress_source"}
	statusBriefFields   = []string{"current_status_update.status_type", "current_status_update.title"}
	portfolioFields     = slices.Concat([]string{"name", "color", "owner.name", "start_on", "due_on", "permalink_url"}, statusBriefFields)
	portfolioItemFields = slices.Concat([]string{"resource_type", "name", "color", "archived", "owner.name", "start_on", "due_on",
		"permalink_url"}, statusBriefFields)
)

// Goals lists the goals of a workspace. Asana answers 402 when the workspace's plan has no
// goals (model.IsPaymentRequired).
func (c *Client) Goals(ctx context.Context, workspaceGID string) ([]model.Goal, error) {
	q := fields(goalFields)
	q.Set("workspace", workspaceGID)
	return list[model.Goal](ctx, c, "/goals", q)
}

// Goal returns one goal, with its rich notes.
func (c *Client) Goal(ctx context.Context, gid string) (*model.Goal, error) {
	var g model.Goal
	return &g, c.get(ctx, "/goals/"+url.PathEscape(gid), fields(append(slices.Clone(goalFields), "html_notes")), &g)
}

// GoalRelationships lists what supports a goal: its subgoals and the projects, portfolios
// and tasks that count toward it.
func (c *Client) GoalRelationships(ctx context.Context, goalGID string) ([]model.GoalRelationship, error) {
	q := fields([]string{"resource_subtype", "contribution_weight", "supporting_resource.name", "supporting_resource.resource_type"})
	q.Set("supported_goal", goalGID)
	return list[model.GoalRelationship](ctx, c, "/goal_relationships", q)
}

// TimePeriods lists the time periods of a workspace (fiscal years, halves, quarters).
func (c *Client) TimePeriods(ctx context.Context, workspaceGID string) ([]model.TimePeriod, error) {
	q := fields(timePeriodFields)
	q.Set("workspace", workspaceGID)
	return list[model.TimePeriod](ctx, c, "/time_periods", q)
}

// Portfolios lists the portfolios of a workspace that the logged-in user owns (Asana only
// lists one's own).
func (c *Client) Portfolios(ctx context.Context, workspaceGID string) ([]model.Portfolio, error) {
	q := fields(portfolioFields)
	q.Set("workspace", workspaceGID)
	q.Set("owner", "me")
	return list[model.Portfolio](ctx, c, "/portfolios", q)
}

// Portfolio returns one portfolio.
func (c *Client) Portfolio(ctx context.Context, gid string) (*model.Portfolio, error) {
	var p model.Portfolio
	return &p, c.get(ctx, "/portfolios/"+url.PathEscape(gid), fields(portfolioFields), &p)
}

// PortfolioItems lists the projects and portfolios in a portfolio, in its order.
func (c *Client) PortfolioItems(ctx context.Context, portfolioGID string) ([]model.PortfolioItem, error) {
	return list[model.PortfolioItem](ctx, c, "/portfolios/"+url.PathEscape(portfolioGID)+"/items", fields(portfolioItemFields))
}
