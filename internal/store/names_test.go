package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
)

func TestResolveTagEdit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t) // has tag g1 "買い物"
	for _, tc := range []struct {
		name        string
		add, remove []string
		want        TagEdit
		fails       bool
		errIs       error
	}{
		{"nothing", nil, nil, TagEdit{}, false, nil},
		{"existing by name", []string{"買い物"}, nil, TagEdit{Add: []TagRef{{GID: "g1"}}}, false, nil},
		{"existing by #name", []string{"#買い物"}, nil, TagEdit{Add: []TagRef{{GID: "g1"}}}, false, nil},
		{"existing by gid", []string{"g1"}, nil, TagEdit{Add: []TagRef{{GID: "g1"}}}, false, nil},
		{"new name", []string{"#急ぎ "}, nil, TagEdit{Add: []TagRef{{Name: "急ぎ"}}}, false, nil},
		{"remove by name", nil, []string{"#買い物"}, TagEdit{Remove: []string{"g1"}}, false, nil},
		{"remove unknown", []string{"急ぎ"}, []string{"ない"}, TagEdit{}, true, ErrNotFound},
		{"empty name", []string{"#"}, nil, TagEdit{}, true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ResolveTagEdit(ctx, tc.add, tc.remove)
			if tc.fails {
				if err == nil || (tc.errIs != nil && !errors.Is(err, tc.errIs)) {
					t.Fatalf("err = %v, want failure (%v)", err, tc.errIs)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestApplyTagEdit(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	now := time.Now()
	e, err := s.ResolveTagEdit(ctx, []string{"急ぎ"}, []string{"買い物"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyTagEdit(ctx, "t1", e, now); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.Tags, []string{"急ぎ"}) {
		t.Fatalf("tags = %v", d.Tags)
	}
	// the tag created offline is found by name next time
	if e, _ := s.ResolveTagEdit(ctx, []string{"急ぎ"}, nil); len(e.Add) != 1 || !IsLocalGID(e.Add[0].GID) {
		t.Fatalf("new tag should be reused: %+v", e)
	}
}

func TestResolveUsersAndProjects(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.UpsertUsers(ctx, []asana.User{{GID: "me", Name: "Me"}, {GID: "u2", Name: "Partner"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ResolveUsers(ctx, []string{"me", "Partner", "u2", "part", "1209999"}); err != nil || !slices.Equal(got, []string{"me", "u2", "u2", "u2", "1209999"}) {
		t.Fatalf("ResolveUsers = %v, %v", got, err)
	}
	if got, err := s.ResolveUsers(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("ResolveUsers(nil) = %v, %v", got, err)
	}
	for _, bad := range []string{"none", "", "誰か"} {
		if _, err := s.ResolveUsers(ctx, []string{"me", bad}); !errors.Is(err, ErrNotFound) {
			t.Errorf("ResolveUsers(%q) err = %v", bad, err)
		}
	}
	if got, err := s.ResolveProjects(ctx, []string{"p3", "新居", "仕事"}); err != nil || !slices.Equal(got, []string{"p3", "p1", "p3"}) {
		t.Fatalf("ResolveProjects = %v, %v", got, err)
	}
	if _, err := s.ResolveProjects(ctx, []string{"[家族]"}); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous project err = %v", err)
	}
}
