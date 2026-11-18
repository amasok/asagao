package app

import (
	"context"
	"slices"

	"github.com/amasok/asagao/internal/readmodel"
)

// Searching Asana as well as the cache (Issue #36): for tasks the cache does not have
// (projects that are not synced, completed tasks older than the sync keeps).

// TaskFinder searches Asana for tasks by text and caches what it finds. sync.Syncer
// satisfies it.
type TaskFinder interface {
	SearchTasks(ctx context.Context, text string, completed *bool, limit int) (readmodel.RemoteSearchResult, error)
}

// RemoteSearchLimit is how many tasks a search asks Asana for when the search has no
// limit of its own (Asana gives at most 100).
const RemoteSearchLimit = 50

// SearchOutcome is what SearchWithAsana found.
type SearchOutcome struct {
	// Tasks are the cache's matches, then the other tasks Asana found that pass the rest of
	// the filter, at most the filter's limit.
	Tasks []readmodel.TaskSummary
	// Found is how many tasks Asana found (before the rest of the filter), and ByName
	// whether it matched them by the start of their name only (typeahead: the workspace's
	// plan lacks the search API).
	Found  int
	ByName bool
	// RemoteErr is why Asana could not be searched (offline, the plan lacks it, ...).
	// Tasks then are the cache's matches only.
	RemoteErr error
}

// SearchWithAsana searches the cache for f.Query, and Asana too (find), caching what Asana
// finds. The rest of f (project, assignee, completed, ...) applies to both. Not reaching
// Asana is not an error: the cache's matches are returned with RemoteErr.
func SearchWithAsana(ctx context.Context, q Query, find TaskFinder, f readmodel.TaskFilter) (SearchOutcome, error) {
	var out SearchOutcome
	limit := RemoteSearchLimit
	if f.Limit > 0 {
		limit = min(f.Limit, 100)
	}
	res, remoteErr := find.SearchTasks(ctx, f.Query, f.Completed, limit)
	local, err := q.ListTasks(ctx, f)
	if err != nil {
		return out, err
	}
	out.Tasks = local
	if remoteErr != nil {
		out.RemoteErr = remoteErr
		return out, nil
	}
	out.Found, out.ByName = len(res.GIDs), res.ByName
	out.Tasks, err = WithFound(ctx, q, f, local, res.GIDs)
	return out, err
}

// WithFound adds to local (the cache's matches for f) the tasks found in Asana (gids,
// already cached) that it lacks and that pass the rest of f, up to f's limit.
func WithFound(ctx context.Context, q Query, f readmodel.TaskFilter, local []readmodel.TaskSummary, gids []string) ([]readmodel.TaskSummary, error) {
	var more []string
	for _, g := range gids {
		if !slices.ContainsFunc(local, func(t readmodel.TaskSummary) bool { return t.GID == g }) {
			more = append(more, g)
		}
	}
	if len(more) == 0 || (f.Limit > 0 && len(local) >= f.Limit) {
		return local, nil
	}
	rf := f
	rf.Query, rf.GIDs, rf.Limit = "", more, 0
	if f.Limit > 0 {
		rf.Limit = f.Limit - len(local)
	}
	found, err := q.ListTasks(ctx, rf)
	if err != nil {
		return nil, err
	}
	return append(local, found...), nil
}
