package asana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	"github.com/amasok/asagao/internal/asana/model"
)

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

// SyncToken returns the new sync token of a 412 from Events (model.SyncToken).
func SyncToken(err error) (string, bool) { return model.SyncToken(err) }

const (
	batchPath = "/batch"
	// MaxBatchActions is how many requests Asana accepts in one POST /batch.
	MaxBatchActions = model.MaxBatchActions
)

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
	tier := c.tier()
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
			tier.mark(&env.Data)
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

// Batch sends up to MaxBatchActions writes in one POST /batch and returns Asana's answer
// to each, in order. An error means the batch as a whole failed (offline, 401, 429, 5xx
// after the client's retries, a refused batch, or a broken answer): Asana may have applied
// any of the writes. A write answered with a status >= 400 has its *Error in its result.
// The client retries a 5xx to /batch, so only writes that are harmless to repeat belong
// in one.
func (c *Client) Batch(ctx context.Context, writes []model.BatchWrite) ([]model.BatchWriteResult, error) {
	if len(writes) == 0 {
		return nil, nil
	}
	if len(writes) > MaxBatchActions {
		return nil, fmt.Errorf("asana: batch: %d requests (at most %d)", len(writes), MaxBatchActions)
	}
	tier := c.tier()
	taskFields := tier.fields(TaskFields)
	actions := make([]batchAction, len(writes))
	wantTask := make([]bool, len(writes))
	for i, w := range writes {
		act, wt, err := batchActionFor(w, taskFields)
		if err != nil {
			return nil, err
		}
		actions[i] = act
		wantTask[i] = wt
	}
	var results []batchResult
	if _, err := c.do(ctx, http.MethodPost, batchPath, nil, map[string]any{"actions": actions}, &results); err != nil {
		return nil, err
	}
	if len(results) != len(writes) {
		return nil, fmt.Errorf("asana: batch: %d results for %d requests", len(results), len(writes))
	}
	out := make([]model.BatchWriteResult, len(writes))
	for i, r := range results {
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			status := r.StatusCode
			if status < 400 { // not an answer a write gets: treat it like a server error
				status = http.StatusBadGateway
			}
			out[i].Err = c.newError(status, r.Body)
			continue
		}
		if !wantTask[i] {
			continue
		}
		var env struct {
			Data Task `json:"data"`
		}
		if err := json.Unmarshal(r.Body, &env); err != nil {
			return nil, fmt.Errorf("asana: batch: decode task: %w", err)
		}
		tier.mark(&env.Data)
		out[i].Task = &env.Data
	}
	return out, nil
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
