package asana

import (
	"context"
	"net/http"
	"net/url"
)

// Time tracking entries of tasks (#6). Asana offers them on paid plans: the writes answer
// 402 without one (the reads may too). Only the duration and the day are written: the
// description, billable status and categories need the Timesheets & Budgets add-on, and
// sending them without it would answer 402 for the whole entry.
var (
	// timeEntryListFields are what a list of entries can give (the compact form).
	timeEntryListFields = []string{"duration_minutes", "entered_on", "assignee.name", "created_by.name", "attributable_to.name", "categories.name"}
	// timeEntryFields are what one entry gives (with when it was created).
	timeEntryFields = append([]string{"created_at"}, timeEntryListFields...)
)

// TimeEntries returns the time tracking entries of a task (GET /tasks/{gid}/time_tracking_entries).
func (c *Client) TimeEntries(ctx context.Context, taskGID string) ([]TimeEntry, error) {
	return list[TimeEntry](ctx, c, "/tasks/"+url.PathEscape(taskGID)+"/time_tracking_entries", fields(timeEntryListFields))
}

// TimeEntry returns one time tracking entry (GET /time_tracking_entries/{gid}).
func (c *Client) TimeEntry(ctx context.Context, gid string) (*TimeEntry, error) {
	var e TimeEntry
	return &e, c.get(ctx, "/time_tracking_entries/"+url.PathEscape(gid), fields(timeEntryFields), &e)
}

// CreateTimeEntry records minutes on a task for the day enteredOn ("YYYY-MM-DD"; "" is
// today for Asana) as the logged-in user (POST /tasks/{gid}/time_tracking_entries).
func (c *Client) CreateTimeEntry(ctx context.Context, taskGID string, minutes int, enteredOn string) (*TimeEntry, error) {
	body := map[string]any{"duration_minutes": minutes}
	if enteredOn != "" {
		body["entered_on"] = enteredOn
	}
	var e TimeEntry
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/time_tracking_entries", fields(timeEntryFields), body, &e)
	return &e, err
}

// UpdateTimeEntry changes the minutes and the day of an entry (PUT /time_tracking_entries/{gid}).
func (c *Client) UpdateTimeEntry(ctx context.Context, gid string, minutes int, enteredOn string) (*TimeEntry, error) {
	body := map[string]any{"duration_minutes": minutes, "entered_on": enteredOn}
	var e TimeEntry
	_, err := c.do(ctx, http.MethodPut, "/time_tracking_entries/"+url.PathEscape(gid), fields(timeEntryFields), body, &e)
	return &e, err
}

// DeleteTimeEntry deletes an entry (DELETE /time_tracking_entries/{gid}).
func (c *Client) DeleteTimeEntry(ctx context.Context, gid string) error {
	_, err := c.do(ctx, http.MethodDelete, "/time_tracking_entries/"+url.PathEscape(gid), nil, nil, nil)
	return err
}
