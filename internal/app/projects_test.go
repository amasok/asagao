package app

import (
	"context"
	"errors"
	"maps"
	"testing"
	"time"
)

// projectStore records the project updates and can fail them.
type projectStore struct {
	*fakeStore
	err     error
	gids    []string
	changes []map[string]any
	at      []time.Time
}

func (f *projectStore) UpdateProjectLocal(_ context.Context, gid string, changes map[string]any, at time.Time) error {
	f.log("UpdateProjectLocal %s", gid)
	f.gids, f.changes, f.at = append(f.gids, gid), append(f.changes, maps.Clone(changes)), append(f.at, at)
	return f.err
}

func newProjects() (*projectStore, *Projects) {
	st := &projectStore{fakeStore: newFake()}
	clock := 0
	return st, &Projects{Store: st, Text: &fakeText{}, Clock: func() time.Time { clock++; return now.Add(time.Duration(clock-1) * 24 * time.Hour) }}
}

func TestProjectUpdateResolvesEachField(t *testing.T) {
	st, projects := newProjects()
	gid, u, err := projects.Update(context.Background(), "新居", UpdateProjectInput{
		Notes: ptr("**目的**"), Owner: ptr("花子"), StartOn: ptr("tomorrow"), DueOn: ptr("+3d"), Color: ptr("dark-red"),
	})
	if err != nil || gid != "p1" || u.Count() != 5 {
		t.Fatalf("Update = %q %+v %v", gid, u, err)
	}
	want := map[string]any{"notes": "plain:**目的**", "html_notes": "<body>**目的**</body>", "owner": "u-hanako",
		"start_on": "2026-11-17", "due_on": "2026-11-19", "color": "dark-red"}
	if len(st.changes) != 1 || !maps.Equal(st.changes[0], want) || st.gids[0] != "p1" {
		t.Fatalf("changes = %v, want %v", st.changes, want)
	}
	// The clock is read once: relative dates and the write are on the same day.
	if !st.at[0].Equal(now) {
		t.Fatalf("written at %v, want %v", st.at[0], now)
	}
}

func TestProjectUpdateClears(t *testing.T) {
	st, projects := newProjects()
	if _, _, err := projects.Update(context.Background(), "p1x", UpdateProjectInput{Owner: ptr("")}); err == nil {
		t.Fatal("unknown project accepted")
	}
	st.projects["p1"] = "p1"
	if _, _, err := projects.Update(context.Background(), "p1", UpdateProjectInput{Owner: ptr(""), StartOn: ptr(""), DueOn: ptr("")}); err != nil {
		t.Fatal(err)
	}
	if want := map[string]any{"owner": "", "start_on": "", "due_on": ""}; !maps.Equal(st.changes[0], want) {
		t.Fatalf("changes = %v", st.changes[0])
	}
}

func TestProjectUpdateInvalidWritesNothing(t *testing.T) {
	for field, in := range map[string]UpdateProjectInput{
		"start_on": {StartOn: ptr("いつか"), Owner: ptr("誰か")},
		"due_on":   {DueOn: ptr("12/31")},
		"owner":    {Owner: ptr("誰か"), DueOn: ptr("today")},
	} {
		st, projects := newProjects()
		_, _, err := projects.Update(context.Background(), "新居", in)
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
		if len(st.changes) != 0 {
			t.Errorf("%s: written %v", field, st.changes)
		}
	}
}

func TestProjectUpdateNothing(t *testing.T) {
	st, projects := newProjects()
	if _, u, err := projects.Update(context.Background(), "新居", UpdateProjectInput{}); err != nil || u.Count() != 0 || len(st.changes) != 0 {
		t.Fatalf("empty update: %+v %v %v", u, err, st.changes)
	}
	st.err = errors.New("boom")
	if _, _, err := projects.Update(context.Background(), "新居", UpdateProjectInput{Color: ptr("none")}); err != st.err {
		t.Fatalf("store error = %v", err)
	}
}
