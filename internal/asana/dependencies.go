package asana

import (
	"context"
	"net/http"
	"net/url"
)

// TaskRef is a task referenced by another one (a dependency or dependent).
type TaskRef struct {
	GID             string `json:"gid"`
	Name            string `json:"name,omitempty"`
	Completed       bool   `json:"completed,omitempty"`
	ResourceSubtype string `json:"resource_subtype,omitempty"`
}

var taskRefFields = []string{"name", "completed", "resource_subtype"}

// Dependencies lists the tasks taskGID waits for (GET /tasks/{gid}/dependencies).
func (c *Client) Dependencies(ctx context.Context, taskGID string) ([]TaskRef, error) {
	return list[TaskRef](ctx, c, "/tasks/"+url.PathEscape(taskGID)+"/dependencies", fields(taskRefFields))
}

// Dependents lists the tasks waiting for taskGID (GET /tasks/{gid}/dependents).
func (c *Client) Dependents(ctx context.Context, taskGID string) ([]TaskRef, error) {
	return list[TaskRef](ctx, c, "/tasks/"+url.PathEscape(taskGID)+"/dependents", fields(taskRefFields))
}

// AddDependencies makes taskGID wait for deps (POST /tasks/{gid}/addDependencies). Tasks
// that are dependencies already are left as they are.
func (c *Client) AddDependencies(ctx context.Context, taskGID string, deps []string) error {
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/addDependencies", nil, map[string]any{"dependencies": deps}, nil)
	return err
}

// RemoveDependencies stops taskGID waiting for deps (POST /tasks/{gid}/removeDependencies).
func (c *Client) RemoveDependencies(ctx context.Context, taskGID string, deps []string) error {
	_, err := c.do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(taskGID)+"/removeDependencies", nil, map[string]any{"dependencies": deps}, nil)
	return err
}
