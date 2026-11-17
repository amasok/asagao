package asana

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/amasok/asagao/internal/asana/model"
)

var notificationFields = []string{
	"is_read", "created_at", "created_by.name", "story_resource_subtype",
	"story.text", "story.resource_subtype", "story.created_at", "story.created_by.name",
	"work_objects", "work_objects.name",
}

// MaxNotificationGIDs is how many notifications one markRead / markUnread request accepts
// (model.MaxNotificationGIDs).
const MaxNotificationGIDs = model.MaxNotificationGIDs

// Notifications returns up to max of the user's active (unarchived) notifications in the
// workspace, newest first.
func (c *Client) Notifications(ctx context.Context, workspaceGID string, max int) ([]Notification, error) {
	q := fields(notificationFields)
	q.Set("workspace", workspaceGID)
	var all []Notification
	for len(all) < max {
		q.Set("limit", strconv.Itoa(min(100, max-len(all))))
		var page []Notification
		env, err := c.do(ctx, http.MethodGet, "/users/me/notifications", q, nil, &page)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if env.NextPage == nil || env.NextPage.Offset == "" || len(page) == 0 {
			break
		}
		q.Set("offset", env.NextPage.Offset)
	}
	return all, nil
}

// MarkNotifications marks notifications read (or unread). It returns the gids Asana could
// not update (deleted, nonexistent or not the user's); the others were updated.
func (c *Client) MarkNotifications(ctx context.Context, workspaceGID string, gids []string, read bool) (failed []string, err error) {
	path := "/users/me/notifications/markUnread"
	if read {
		path = "/users/me/notifications/markRead"
	}
	var out struct {
		FailedGIDs []string `json:"failed_gids"`
	}
	body := map[string]any{"workspace": workspaceGID, "notification_gids": gids}
	_, err = c.do(ctx, http.MethodPost, path, url.Values{}, body, &out)
	return out.FailedGIDs, err
}
