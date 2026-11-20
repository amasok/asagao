package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// searchCache is a cache of tasks that ListTasks filters by Query (in the name), GIDs,
// Completed and Limit. The other QueryStore methods are not used.
type searchCache struct {
	QueryStore
	tasks   []readmodel.TaskSummary
	filters []readmodel.TaskFilter
	err     error
}

func (c *searchCache) ListTasks(_ context.Context, f readmodel.TaskFilter) ([]readmodel.TaskSummary, error) {
	c.filters = append(c.filters, f)
	if c.err != nil {
		return nil, c.err
	}
	var out []readmodel.TaskSummary
	for _, t := range c.tasks {
		if f.Query != "" && !strings.Contains(t.Name, f.Query) ||
			f.GIDs != nil && !slices.Contains(f.GIDs, t.GID) ||
			f.Completed != nil && t.Completed != *f.Completed {
			continue
		}
		out = append(out, t)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, nil
}

// fakeTaskFinder "finds" tasks in Asana and caches them (adds them to the cache).
type fakeTaskFinder struct {
	cache    *searchCache
	found    []readmodel.TaskSummary
	byName   bool
	err      error
	limits   []int
	complete []*bool
}

func (f *fakeTaskFinder) SearchTasks(_ context.Context, text string, completed *bool, limit int) (readmodel.RemoteSearchResult, error) {
	f.limits, f.complete = append(f.limits, limit), append(f.complete, completed)
	if f.err != nil {
		return readmodel.RemoteSearchResult{}, f.err
	}
	res := readmodel.RemoteSearchResult{ByName: f.byName}
	for _, t := range f.found {
		if !slices.ContainsFunc(f.cache.tasks, func(c readmodel.TaskSummary) bool { return c.GID == t.GID }) {
			f.cache.tasks = append(f.cache.tasks, t)
		}
		res.GIDs = append(res.GIDs, t.GID)
	}
	return res, nil
}

func taskGIDs(ts []readmodel.TaskSummary) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.GID)
	}
	return out
}

func TestSearchWithAsanaAddsWhatAsanaFound(t *testing.T) {
	ctx := context.Background()
	cache := &searchCache{tasks: []readmodel.TaskSummary{{GID: "t1", Name: "牛乳を買う"}, {GID: "t2", Name: "卵"}}}
	f := &fakeTaskFinder{cache: cache, byName: true, found: []readmodel.TaskSummary{
		{GID: "t1", Name: "牛乳を買う"},                  // the cache has it already
		{GID: "t9", Name: "説明に牛乳とある"},               // matches the cache's search too, once cached
		{GID: "t8", Name: "古いタスク", Completed: true}, // only Asana matched it (its description)
	}}
	open := false
	out, err := SearchWithAsana(ctx, Query{QueryStore: cache}, f, readmodel.TaskFilter{Query: "牛乳"})
	if err != nil || out.RemoteErr != nil {
		t.Fatal(err, out.RemoteErr)
	}
	if !slices.Equal(taskGIDs(out.Tasks), []string{"t1", "t9", "t8"}) || out.Found != 3 || !out.ByName {
		t.Fatalf("out %+v", out)
	}
	if f.limits[0] != RemoteSearchLimit || f.complete[0] != nil {
		t.Fatalf("asked limit %v completed %v", f.limits, f.complete)
	}
	// the rest of the filter applies to what Asana found
	out, err = SearchWithAsana(ctx, Query{QueryStore: cache}, f, readmodel.TaskFilter{Query: "牛乳", Completed: &open, Limit: 500})
	if err != nil || !slices.Equal(taskGIDs(out.Tasks), []string{"t1", "t9"}) {
		t.Fatalf("open only: %+v %v", out, err)
	}
	if f.limits[1] != 100 || f.complete[1] != &open {
		t.Fatalf("asked limit %v completed %v", f.limits, f.complete)
	}
}

func TestSearchWithAsanaKeepsTheLimit(t *testing.T) {
	ctx := context.Background()
	cache := &searchCache{tasks: []readmodel.TaskSummary{{GID: "t1", Name: "牛乳 1"}}}
	f := &fakeTaskFinder{cache: cache, found: []readmodel.TaskSummary{{GID: "t7", Name: "x"}, {GID: "t8", Name: "y"}, {GID: "t9", Name: "z"}}}
	out, err := SearchWithAsana(ctx, Query{QueryStore: cache}, f, readmodel.TaskFilter{Query: "牛乳", Limit: 2})
	if err != nil || !slices.Equal(taskGIDs(out.Tasks), []string{"t1", "t7"}) || out.Found != 3 {
		t.Fatalf("out %+v %v", out, err)
	}
	if f.limits[0] != 2 {
		t.Fatalf("limit %v", f.limits)
	}
	// the cache alone fills the limit: Asana's are not listed
	out, err = SearchWithAsana(ctx, Query{QueryStore: cache}, f, readmodel.TaskFilter{Query: "牛乳", Limit: 1})
	if err != nil || !slices.Equal(taskGIDs(out.Tasks), []string{"t1"}) {
		t.Fatalf("out %+v %v", out, err)
	}
}

func TestSearchWithAsanaOffline(t *testing.T) {
	ctx := context.Background()
	cache := &searchCache{tasks: []readmodel.TaskSummary{{GID: "t1", Name: "牛乳"}}}
	offline := errors.New("dial tcp: no route")
	f := &fakeTaskFinder{cache: cache, err: offline}
	out, err := SearchWithAsana(ctx, Query{QueryStore: cache}, f, readmodel.TaskFilter{Query: "牛乳"})
	if err != nil || !errors.Is(out.RemoteErr, offline) || !slices.Equal(taskGIDs(out.Tasks), []string{"t1"}) {
		t.Fatalf("out %+v %v", out, err)
	}
	// a cache error is an error
	cache.err = errors.New("disk")
	if _, err := SearchWithAsana(ctx, Query{QueryStore: cache}, &fakeTaskFinder{cache: cache}, readmodel.TaskFilter{Query: "x"}); err == nil {
		t.Fatal("cache error lost")
	}
}
