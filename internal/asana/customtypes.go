package asana

import (
	"context"
)

// customTypeFields are the fields of a custom task type with its statuses (#59).
var customTypeFields = []string{"name", "status_options.name", "status_options.color",
	"status_options.enabled", "status_options.completion_state"}

// CustomTypes returns the custom task types a project offers, with their statuses
// (GET /custom_types?project=). A plan without them answers 402.
func (c *Client) CustomTypes(ctx context.Context, projectGID string) ([]CustomType, error) {
	q := fields(customTypeFields)
	q.Set("project", projectGID)
	return list[CustomType](ctx, c, "/custom_types", q)
}
