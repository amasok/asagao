package asana

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Finding what the local cache does not have (Issue #36): the search API (premium only,
// 402 otherwise), typeahead and tasks by their custom ID. Search and typeahead answer with
// compact records; the callers read the tasks themselves with TasksByGID, so that a
// compact record never overwrites a cached task.

// MaxSearchResults is the most results one search or typeahead request gives (Asana's
// limit: neither can be paged).
const MaxSearchResults = 100

func clampCount(n int) string {
	return strconv.Itoa(min(max(n, 1), MaxSearchResults))
}

// SearchTasks searches the workspace's tasks by text (name and description), newest change
// first. completed nil means both open and completed tasks. Without a premium plan Asana
// answers 402 (IsPaymentRequired).
func (c *Client) SearchTasks(ctx context.Context, workspaceGID, text string, completed *bool, limit int) ([]Ref, error) {
	q := fields([]string{"name"})
	q.Set("text", text)
	q.Set("sort_by", "modified_at")
	q.Set("limit", clampCount(limit))
	if completed != nil {
		q.Set("completed", strconv.FormatBool(*completed))
	}
	var refs []Ref
	if err := c.get(ctx, "/workspaces/"+url.PathEscape(workspaceGID)+"/tasks/search", q, &refs); err != nil {
		return nil, err
	}
	return refs, nil
}

// typeahead asks GET /workspaces/{gid}/typeahead for at most count resources of one type
// whose name starts like query.
func typeahead[T any](ctx context.Context, c *Client, workspaceGID, resourceType, query string, count int, f []string) ([]T, error) {
	q := fields(f)
	q.Set("resource_type", resourceType)
	q.Set("query", query)
	q.Set("count", clampCount(count))
	var out []T
	if err := c.get(ctx, "/workspaces/"+url.PathEscape(workspaceGID)+"/typeahead", q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TypeaheadTasks returns tasks whose name starts like query (gid and name only).
func (c *Client) TypeaheadTasks(ctx context.Context, workspaceGID, query string, count int) ([]Ref, error) {
	return typeahead[Ref](ctx, c, workspaceGID, "task", query, count, []string{"name"})
}

// TypeaheadUsers returns users whose name or email starts like query.
func (c *Client) TypeaheadUsers(ctx context.Context, workspaceGID, query string, count int) ([]User, error) {
	return typeahead[User](ctx, c, workspaceGID, "user", query, count, []string{"name", "email"})
}

// TypeaheadTags returns tags whose name starts like query.
func (c *Client) TypeaheadTags(ctx context.Context, workspaceGID, query string, count int) ([]Tag, error) {
	tags, err := typeahead[Tag](ctx, c, workspaceGID, "tag", query, count, tagFields)
	if err != nil {
		return nil, err
	}
	for i := range tags {
		if (tags[i].Workspace == nil || tags[i].Workspace.GID == "") && workspaceGID != "" {
			tags[i].Workspace = &Ref{GID: workspaceGID}
		}
	}
	return tags, nil
}

// errBadCustomID is returned for a custom ID that cannot be a path segment.
var errBadCustomID = errors.New("asana: invalid custom ID")

// TaskByCustomID reads the task with the custom ID (e.g. "ENG-123") in the workspace.
// Asana answers 404 when there is none, and also 500 for some IDs it does not know (an
// uppercase prefix no project has, seen on a real workspace): a 5xx is therefore not
// retried here, only a 429.
func (c *Client) TaskByCustomID(ctx context.Context, workspaceGID, customID string) (*Task, error) {
	if customID == "" || customID == "." || customID == ".." {
		return nil, errBadCustomID
	}
	ts, err := readTasks(c, false, func(f []string) ([]Task, error) {
		var t Task
		path := "/workspaces/" + url.PathEscape(workspaceGID) + "/tasks/custom_id/" + url.PathEscape(customID)
		_, err := c.doWith(ctx, http.MethodGet, path, fields(f), nil, &t, func(status int) bool { return status == http.StatusTooManyRequests })
		if err != nil {
			return nil, err
		}
		return []Task{t}, nil
	})
	if err != nil {
		return nil, err
	}
	return &ts[0], nil
}
