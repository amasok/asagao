package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// Looking names the cache does not know up in Asana (Issue #36).

// growingStore is a quick-add cache that the finder adds to.
type growingStore struct {
	users []readmodel.UserContact
	tags  []readmodel.TagCount
}

func (s *growingStore) MeGID(context.Context) (string, error) { return "u-me", nil }
func (s *growingStore) ListUserContacts(context.Context) ([]readmodel.UserContact, error) {
	return s.users, nil
}
func (s *growingStore) ListTags(context.Context) ([]readmodel.TagCount, error) { return s.tags, nil }

// fakeFinder adds what Asana knows (users and tags by query) to the store.
type fakeFinder struct {
	st    *growingStore
	users map[string][]readmodel.UserContact
	tags  map[string][]readmodel.TagCount
	err   error
	calls []string
	// onUsers also adds to the fake cache of Tasks (fakeStore.users).
	onUsers func(q string) int
}

func (f *fakeFinder) FindUsers(_ context.Context, q string) (int, error) {
	f.calls = append(f.calls, "@"+q)
	if f.err != nil {
		return 0, f.err
	}
	if f.onUsers != nil {
		return f.onUsers(q), nil
	}
	found := f.users[q]
	f.st.users = append(f.st.users, found...)
	return len(found), nil
}

func (f *fakeFinder) FindTags(_ context.Context, q string) (int, error) {
	f.calls = append(f.calls, "#"+q)
	if f.err != nil {
		return 0, f.err
	}
	found := f.tags[q]
	f.st.tags = append(f.st.tags, found...)
	return len(found), nil
}

func TestReadQuickAddFindingLooksUpUnknownNames(t *testing.T) {
	ctx := context.Background()
	st := &growingStore{
		users: []readmodel.UserContact{{GID: "u-me", Name: "Me"}, {GID: "u-taro", Name: "Taro"}},
		tags:  []readmodel.TagCount{{GID: "g-buy", Name: "買い物"}},
	}
	f := &fakeFinder{st: st,
		users: map[string][]readmodel.UserContact{"hanako": {{GID: "u-hanako", Name: "Hanako Yamada"}}},
		tags:  map[string][]readmodel.TagCount{"新しい": {{GID: "g-new", Name: "新しい"}}},
	}
	r, err := ReadQuickAddFinding(ctx, st, f, "牛乳を買う @hanako #新しい #買い物 明日", now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Name != "牛乳を買う" || r.Assignee == nil || r.Assignee.GID != "u-hanako" || len(r.Tags) != 2 || r.Unread != "" {
		t.Fatalf("result %+v", r)
	}
	// only what the cache did not know, once each; names known to the cache are not asked
	if !slices.Equal(f.calls, []string{"#新しい", "@hanako"}) {
		t.Fatalf("calls %v", f.calls)
	}
}

func TestReadQuickAddFindingWithoutAsana(t *testing.T) {
	ctx := context.Background()
	st := &growingStore{users: []readmodel.UserContact{{GID: "u-me", Name: "Me"}}}
	for name, f := range map[string]NameFinder{
		"nil":     nil,
		"offline": &fakeFinder{st: st, err: errors.New("dial tcp: offline")},
		"unknown": &fakeFinder{st: st},
	} {
		r, err := ReadQuickAddFinding(ctx, st, f, "牛乳を買う @nobody", now)
		if err != nil || r.Name != "牛乳を買う @nobody" || r.Unread != "@nobody" || r.UnreadReason != reasonNoUser {
			t.Errorf("%s: %+v %v", name, r, err)
		}
	}
}

func TestReadQuickAddFindingAsksOnlyForNoMatch(t *testing.T) {
	ctx := context.Background()
	st := &growingStore{users: []readmodel.UserContact{
		{GID: "u-me", Name: "Me"}, {GID: "u-ta1", Name: "Taro"}, {GID: "u-ta2", Name: "Tamaki"}}}
	f := &fakeFinder{st: st}
	for _, line := range []string{
		"a @ta",      // several matches: Asana would not narrow it down
		"a @me @me",  // a second assignee
		"a 明日 明後日",   // a second due date
		"a due:来週",   // not a date
		"a @taro 本文", // ordinary word: nothing to look up
	} {
		if _, err := ReadQuickAddFinding(ctx, st, f, line, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("asked Asana: %v", f.calls)
	}
}

// A finder that keeps "adding" something that never matches is asked once per token, so
// reading always ends.
func TestReadQuickAddFindingAsksEachTokenOnce(t *testing.T) {
	ctx := context.Background()
	st := &growingStore{users: []readmodel.UserContact{{GID: "u-me", Name: "Me"}}}
	f := &fakeFinder{st: st, users: map[string][]readmodel.UserContact{"x": {{GID: "u-other", Name: "Other"}}}}
	r, err := ReadQuickAddFinding(ctx, st, f, "a @x", now)
	if err != nil || r.Unread != "@x" || !slices.Equal(f.calls, []string{"@x"}) {
		t.Fatalf("%+v %v calls %v", r, err, f.calls)
	}
}

func TestUpdateAssigneeLooksUpUnknownUser(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	tasks := newTasks(st, &fakeText{})
	f := &fakeFinder{onUsers: func(q string) int {
		if q == "hanako@example.com" {
			st.users[q] = "u-new"
			return 1
		}
		return 0
	}}
	tasks.Names = f
	who := "hanako@example.com"
	u, err := tasks.ResolveUpdate(ctx, UpdateTaskInput{Assignee: &who})
	if err != nil || u.Assignee == nil || *u.Assignee != "u-new" {
		t.Fatalf("%+v %v", u, err)
	}
	// a user the cache knows is not asked for
	known := "花子"
	if _, err := tasks.ResolveUpdate(ctx, UpdateTaskInput{Assignee: &known}); err != nil {
		t.Fatal(err)
	}
	// nobody in Asana either: the cache's error, as it was
	nobody := "nobody"
	_, err = tasks.ResolveUpdate(ctx, UpdateTaskInput{Assignee: &nobody})
	var fe *FieldError
	if !errors.As(err, &fe) || !errors.Is(err, errNotFound) {
		t.Fatalf("nobody: %v", err)
	}
	if !slices.Equal(f.calls, []string{"@hanako@example.com", "@nobody"}) {
		t.Fatalf("calls %v", f.calls)
	}
	// creating with an unknown assignee also looks it up
	f.onUsers = func(q string) int { st.users[q] = "u-jiro"; return 1 }
	if _, err := tasks.Create(ctx, CreateTaskInput{Name: "x", Assignee: "jiro"}); err != nil {
		t.Fatal(err)
	}
	if got := st.created[len(st.created)-1].AssigneeGID; got != "u-jiro" {
		t.Fatalf("created with %q", got)
	}
}
