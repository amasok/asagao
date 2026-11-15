package asana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"
)

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

var eventFields = []string{"action", "resource", "resource.resource_type", "parent", "parent.resource_type", "change.field", "change.action", "created_at"}

// Events returns the changes to resource (a project or task) since the call that returned
// token. Without a token, or when the token has expired (about 24 hours), Asana answers
// 412 with a new token: SyncToken returns it, and the caller must fetch the resource's
// data in full, as the changes before it are not known.
func (c *Client) Events(ctx context.Context, resource, token string) (EventPage, error) {
	q := fields(eventFields)
	q.Set("resource", resource)
	if token != "" {
		q.Set("sync", token)
	}
	var page EventPage
	env, err := c.do(ctx, http.MethodGet, "/events", q, nil, &page.Events)
	if err != nil {
		return page, err
	}
	page.Sync, page.HasMore = env.Sync, env.HasMore
	if page.Sync == "" {
		return page, errors.New("asana: events: no sync token in the response")
	}
	return page, nil
}

// SyncToken returns the new sync token of a 412 from Events (no token, or it expired).
func SyncToken(err error) (string, bool) {
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusPreconditionFailed && e.Sync != "" {
		return e.Sync, true
	}
	return "", false
}

const (
	batchPath = "/batch"
	// MaxBatchActions is how many requests Asana accepts in one POST /batch.
	MaxBatchActions = 10
)

type batchAction struct {
	Method       string         `json:"method"`
	RelativePath string         `json:"relative_path"`
	Options      map[string]any `json:"options,omitempty"`
}

type batchResult struct {
	StatusCode int             `json:"status_code"`
	Body       json.RawMessage `json:"body"`
}

// TasksByGID fetches tasks by gid, up to MaxBatchActions per request (POST /batch, which
// only reads here). Tasks Asana does not have, or no longer shows us (404, 403), are
// returned in gone. If the batch endpoint is refused, the tasks are read one by one.
func (c *Client) TasksByGID(ctx context.Context, gids []string) (tasks []Task, gone []string, err error) {
	for chunk := range slices.Chunk(gids, MaxBatchActions) {
		t, g, err := c.tasksBatch(ctx, chunk)
		if err != nil {
			return tasks, gone, err
		}
		tasks, gone = append(tasks, t...), append(gone, g...)
	}
	return tasks, gone, nil
}

func (c *Client) tasksBatch(ctx context.Context, gids []string) (tasks []Task, gone []string, err error) {
	plain := c.plainTasks.Load()
	f := c.taskFields(false)
	actions := make([]batchAction, len(gids))
	for i, gid := range gids {
		actions[i] = batchAction{Method: "get", RelativePath: "/tasks/" + url.PathEscape(gid), Options: map[string]any{"fields": f}}
	}
	var results []batchResult
	_, err = c.do(ctx, http.MethodPost, batchPath, nil, map[string]any{"actions": actions}, &results)
	var apiErr *Error
	if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 &&
		apiErr.Status != http.StatusUnauthorized && apiErr.Status != http.StatusTooManyRequests {
		return c.tasksOneByOne(ctx, gids) // e.g. batch not allowed for this token
	}
	if err != nil {
		return nil, nil, err
	}
	if len(results) != len(gids) {
		return nil, nil, fmt.Errorf("asana: batch: %d results for %d requests", len(results), len(gids))
	}
	for i, r := range results {
		switch {
		case r.StatusCode >= 200 && r.StatusCode < 300:
			var env struct {
				Data Task `json:"data"`
			}
			if err := json.Unmarshal(r.Body, &env); err != nil {
				return nil, nil, fmt.Errorf("asana: batch: decode task: %w", err)
			}
			env.Data.Partial = plain
			tasks = append(tasks, env.Data)
		case r.StatusCode == http.StatusNotFound || r.StatusCode == http.StatusForbidden:
			gone = append(gone, gids[i])
		case r.StatusCode == http.StatusBadRequest || r.StatusCode == http.StatusPaymentRequired:
			// e.g. the optional fields refused: Task falls back to the plain fields
			t, g, err := c.tasksOneByOne(ctx, gids[i:i+1])
			if err != nil {
				return nil, nil, err
			}
			tasks, gone = append(tasks, t...), append(gone, g...)
		default: // 429, 5xx, 401: report it like a request of its own
			return nil, nil, c.newError(r.StatusCode, r.Body)
		}
	}
	return tasks, gone, nil
}

func (c *Client) tasksOneByOne(ctx context.Context, gids []string) (tasks []Task, gone []string, err error) {
	for _, gid := range gids {
		t, err := c.Task(ctx, gid)
		var apiErr *Error
		switch {
		case err == nil:
			tasks = append(tasks, *t)
		case errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden):
			gone = append(gone, gid)
		default:
			return nil, nil, err
		}
	}
	return tasks, gone, nil
}
