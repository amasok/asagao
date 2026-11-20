package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

func ts(gid, name string, completed bool) readmodel.TaskSummary {
	return readmodel.TaskSummary{GID: gid, Name: name, Completed: completed}
}

// Edges of mixing the cache's matches with what Asana found (#36).
func TestSearchWithAsanaAttack(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name      string
		cache     []readmodel.TaskSummary
		found     []readmodel.TaskSummary
		f         readmodel.TaskFilter
		want      []string
		wantLimit int // limit asked of Asana
	}{
		{"found already cached is not repeated", []readmodel.TaskSummary{ts("1", "牛乳", false)}, []readmodel.TaskSummary{ts("1", "牛乳", false)},
			readmodel.TaskFilter{Query: "牛乳"}, []string{"1"}, RemoteSearchLimit},
		{"limit reached by the cache: nothing added", []readmodel.TaskSummary{ts("1", "牛乳", false), ts("2", "牛乳2", false)},
			[]readmodel.TaskSummary{ts("3", "x", false)}, readmodel.TaskFilter{Query: "牛乳", Limit: 2}, []string{"1", "2"}, 2},
		{"limit leaves room for one", []readmodel.TaskSummary{ts("1", "牛乳", false)},
			[]readmodel.TaskSummary{ts("3", "x", false), ts("4", "y", false)}, readmodel.TaskFilter{Query: "牛乳", Limit: 2}, []string{"1", "3"}, 2},
		{"limit over 100 is asked as 100", nil, nil, readmodel.TaskFilter{Query: "q", Limit: 5000}, nil, 100},
		{"completed filter applies to found", nil,
			[]readmodel.TaskSummary{ts("5", "a", true), ts("6", "b", false)}, readmodel.TaskFilter{Query: "q", Completed: &no}, []string{"6"}, RemoteSearchLimit},
		{"completed true", nil,
			[]readmodel.TaskSummary{ts("5", "a", true), ts("6", "b", false)}, readmodel.TaskFilter{Query: "q", Completed: &yes}, []string{"5"}, RemoteSearchLimit},
		{"asana found nothing", []readmodel.TaskSummary{ts("1", "q", false)}, nil, readmodel.TaskFilter{Query: "q"}, []string{"1"}, RemoteSearchLimit},
		{"negative limit is no limit", nil, []readmodel.TaskSummary{ts("5", "a", false), ts("6", "b", false)}, readmodel.TaskFilter{Query: "q", Limit: -1}, []string{"5", "6"}, RemoteSearchLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &searchCache{tasks: slices.Clone(tc.cache)}
			find := &fakeTaskFinder{cache: cache, found: tc.found}
			out, err := SearchWithAsana(context.Background(), Query{QueryStore: cache}, find, tc.f)
			if err != nil || out.RemoteErr != nil {
				t.Fatal(err, out.RemoteErr)
			}
			if got := taskGIDs(out.Tasks); !slices.Equal(got, tc.want) {
				t.Fatalf("tasks %v, want %v", got, tc.want)
			}
			if tc.f.Limit > 0 && len(out.Tasks) > tc.f.Limit {
				t.Fatalf("over the limit: %d", len(out.Tasks))
			}
			if len(find.limits) != 1 || find.limits[0] != tc.wantLimit {
				t.Fatalf("asked limit %v, want %d", find.limits, tc.wantLimit)
			}
		})
	}
}

// Whatever Asana answers, the cache's matches come back; the filter the caller passed is not changed.
func TestSearchWithAsanaErrorsKeepLocal(t *testing.T) {
	for _, e := range []error{errors.New("offline"), context.DeadlineExceeded, context.Canceled} {
		cache := &searchCache{tasks: []readmodel.TaskSummary{ts("1", "牛乳", false)}}
		find := &fakeTaskFinder{cache: cache, err: e}
		f := readmodel.TaskFilter{Query: "牛乳", Limit: 3, GIDs: nil}
		out, err := SearchWithAsana(context.Background(), Query{QueryStore: cache}, find, f)
		if err != nil || !errors.Is(out.RemoteErr, e) || !slices.Equal(taskGIDs(out.Tasks), []string{"1"}) {
			t.Fatalf("%v: %+v %v", e, out, err)
		}
		if f.GIDs != nil || f.Query != "牛乳" || f.Limit != 3 {
			t.Fatalf("filter changed: %+v", f)
		}
	}
	// a cache failure is an error even when Asana answered
	cache := &searchCache{err: errors.New("db")}
	if _, err := SearchWithAsana(context.Background(), Query{QueryStore: cache}, &fakeTaskFinder{cache: cache}, readmodel.TaskFilter{Query: "q"}); err == nil {
		t.Fatal("cache error swallowed")
	}
}

// WithFound with an empty (not nil) list adds nothing and never asks the cache for "all tasks".
func TestWithFoundEmptyList(t *testing.T) {
	cache := &searchCache{tasks: []readmodel.TaskSummary{ts("1", "a", false), ts("2", "b", false)}}
	local := []readmodel.TaskSummary{ts("1", "a", false)}
	for _, g := range [][]string{nil, {}} {
		got, err := WithFound(context.Background(), Query{QueryStore: cache}, readmodel.TaskFilter{Query: "a"}, local, g)
		if err != nil || !slices.Equal(taskGIDs(got), []string{"1"}) {
			t.Fatalf("%v: %v %v", g, got, err)
		}
	}
	if len(cache.filters) != 0 {
		t.Fatalf("cache asked: %+v", cache.filters)
	}
	// a found list whose tasks are all filtered out: GIDs set (not nil), so 2 does not leak in
	got, _ := WithFound(context.Background(), Query{QueryStore: cache}, readmodel.TaskFilter{Query: "a", Completed: boolPtr(true)}, nil, []string{"2"})
	if len(got) != 0 {
		t.Fatalf("leaked %v", got)
	}
}

func boolPtr(b bool) *bool { return &b }

// --- quick add ---

type qaStore struct {
	users []readmodel.UserContact
	tags  []readmodel.TagCount
}

func (q *qaStore) MeGID(context.Context) (string, error) { return "me", nil }
func (q *qaStore) ListUserContacts(context.Context) ([]readmodel.UserContact, error) {
	return q.users, nil
}
func (q *qaStore) ListTags(context.Context) ([]readmodel.TagCount, error) { return q.tags, nil }

type countingFinder struct {
	st        *qaStore
	users     []readmodel.UserContact // what Asana knows
	tags      []readmodel.TagCount
	err       error
	pretend   bool // says it added something although nothing matches now
	userCalls []string
	tagCalls  []string
}

func (c *countingFinder) FindUsers(_ context.Context, q string) (int, error) {
	c.userCalls = append(c.userCalls, q)
	if c.err != nil {
		return 0, c.err
	}
	n := 0
	for _, u := range c.users {
		if !slices.ContainsFunc(c.st.users, func(x readmodel.UserContact) bool { return x.GID == u.GID }) {
			c.st.users = append(c.st.users, u)
			n++
		}
	}
	if c.pretend {
		return 1, nil
	}
	return n, nil
}

func (c *countingFinder) FindTags(_ context.Context, q string) (int, error) {
	c.tagCalls = append(c.tagCalls, q)
	if c.err != nil {
		return 0, c.err
	}
	n := 0
	for _, g := range c.tags {
		if !slices.ContainsFunc(c.st.tags, func(x readmodel.TagCount) bool { return x.GID == g.GID }) {
			c.st.tags = append(c.st.tags, g)
			n++
		}
	}
	if c.pretend {
		return 1, nil
	}
	return n, nil
}

func TestQuickAddFindingAttack(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	ctx := context.Background()
	t.Run("finder says added but nothing matches: asked once, no loop", func(t *testing.T) {
		st := &qaStore{}
		f := &countingFinder{st: st, pretend: true}
		r, err := ReadQuickAddFinding(ctx, st, f, "買う @zzzz", now)
		if err != nil || r.Unread != "@zzzz" || len(f.userCalls) != 1 {
			t.Fatalf("%+v %v calls %v", r, err, f.userCalls)
		}
	})
	t.Run("several matches are not asked", func(t *testing.T) {
		st := &qaStore{users: []readmodel.UserContact{{GID: "1", Name: "Hanako A"}, {GID: "2", Name: "Hanako B"}}}
		f := &countingFinder{st: st}
		r, _ := ReadQuickAddFinding(ctx, st, f, "買う @hanako", now)
		if r.Unread != "@hanako" || len(f.userCalls)+len(f.tagCalls) != 0 {
			t.Fatalf("%+v %v %v", r, f.userCalls, f.tagCalls)
		}
	})
	t.Run("two unknown tokens are each asked once, then read", func(t *testing.T) {
		st := &qaStore{}
		f := &countingFinder{st: st, users: []readmodel.UserContact{{GID: "7", Name: "Hanako Yamada"}}, tags: []readmodel.TagCount{{GID: "g", Name: "buy"}}}
		r, err := ReadQuickAddFinding(ctx, st, f, "task #buy @hanako", now)
		if err != nil || r.Unread != "" || r.Assignee == nil || len(r.Tags) != 1 {
			t.Fatalf("%+v %v", r, err)
		}
		if len(f.userCalls) != 1 || len(f.tagCalls) != 1 {
			t.Fatalf("calls %v %v", f.userCalls, f.tagCalls)
		}
	})
	t.Run("asana error keeps the cache's reading, no retry", func(t *testing.T) {
		st := &qaStore{}
		f := &countingFinder{st: st, err: errors.New("offline")}
		r, err := ReadQuickAddFinding(ctx, st, f, "task @hanako", now)
		if err != nil || r.Unread != "@hanako" || len(f.userCalls) != 1 {
			t.Fatalf("%+v %v %v", r, err, f.userCalls)
		}
	})
	t.Run("nil finder", func(t *testing.T) {
		r, err := ReadQuickAddFinding(ctx, &qaStore{}, nil, "task @hanako", now)
		if err != nil || r.Unread != "@hanako" {
			t.Fatalf("%+v %v", r, err)
		}
	})
	t.Run("a bare @ or # asks nothing useful", func(t *testing.T) {
		st := &qaStore{}
		f := &countingFinder{st: st}
		for _, line := range []string{"task @", "task #", "task @@", "task ＠hanako", `task \@hanako`} {
			if _, err := ReadQuickAddFinding(ctx, st, f, line, now); err != nil {
				t.Fatal(err)
			}
		}
		for _, q := range append(f.userCalls, f.tagCalls...) {
			if q == "" {
				t.Fatal("empty query asked")
			}
		}
		for _, q := range f.userCalls {
			if q == "hanako" {
				return
			}
		}
		t.Fatalf("full-width ＠hanako was not asked: %v", f.userCalls)
	})
}
