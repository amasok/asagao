package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	ref := func(gid, name string) *asana.Ref { return &asana.Ref{GID: gid, Name: name} }
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.SetSyncState(ctx, KeyMeGID, "me"))
	must(s.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "p1", Name: "[家族]新居プロジェクト", Members: []asana.Ref{{GID: "me"}}},
		{GID: "p2", Name: "[家族]事務/雑務", Members: []asana.Ref{{GID: "me"}}},
		{GID: "p3", Name: "仕事"},
	}, "me"))
	must(s.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "手続き"}}))
	must(s.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", Notes: "リビング用", DueOn: "2026-11-20", Assignee: ref("me", "Me"),
			Memberships: []asana.Membership{{Project: ref("p1", ""), Section: ref("s1", "")}}, Tags: []asana.Ref{{GID: "g1", Name: "買い物"}}},
		{GID: "t2", Name: "転居届を出す", DueOn: "2026-11-10", Assignee: ref("me", "Me"),
			Memberships: []asana.Membership{{Project: ref("p1", ""), Section: ref("s2", "")}}},
		{GID: "t3", Name: "電気の契約", Assignee: ref("u2", "Partner"),
			Memberships: []asana.Membership{{Project: ref("p1", ""), Section: ref("s2", "")}}},
		{GID: "t4", Name: "年末調整", Completed: true, DueOn: "2026-11-01", Assignee: ref("me", "Me"),
			Memberships: []asana.Membership{{Project: ref("p2", "")}}},
		{GID: "t1a", Name: "サイズを測る", Parent: ref("t1", "")},
	}))
	must(s.ReplaceStories(ctx, "t3", []asana.Story{
		{GID: "c1", Type: "comment", Text: "東京電力とガスのセット割を検討", CreatedBy: ref("u2", "Partner")},
	}, time.Now()))
	return s
}

func gids(ts []TaskSummary) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.GID)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestListTasksFilters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	f, tr := false, true
	for _, tc := range []struct {
		name   string
		filter TaskFilter
		want   []string
	}{
		{"my open tasks ordered by due date", TaskFilter{AssigneeGID: "me", Completed: &f}, []string{"t2", "t1"}},
		{"project, all assignees", TaskFilter{ProjectGID: "p1", Completed: &f}, []string{"t2", "t1", "t3"}},
		{"section", TaskFilter{SectionGID: "s2"}, []string{"t2", "t3"}},
		{"completed only", TaskFilter{Completed: &tr}, []string{"t4"}},
		{"due before (overdue)", TaskFilter{DueBefore: "2026-11-12", Completed: &f}, []string{"t2"}},
		{"due after", TaskFilter{DueAfter: "2026-11-15"}, []string{"t1"}},
		{"tag by name", TaskFilter{Tag: "買い物"}, []string{"t1"}},
		{"subtasks of parent", TaskFilter{ParentGID: "t1"}, []string{"t1a"}},
		{"limit", TaskFilter{AssigneeGID: "me", Limit: 1}, []string{"t2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ListTasks(ctx, tc.filter)
			if err != nil {
				t.Fatal(err)
			}
			if !equal(gids(got), tc.want) {
				t.Fatalf("got %v, want %v", gids(got), tc.want)
			}
		})
	}
}

func TestSearchJapanese(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		query string
		want  []string
	}{
		{"ソファ", []string{"t1"}},   // 3 chars: trigram FTS
		{"リビング", []string{"t1"}},  // notes
		{"転居", []string{"t2"}},    // 2 chars: LIKE fallback
		{"電気", []string{"t3"}},    // 2 chars
		{"セット割", []string{"t3"}},  // comment text via stories_fts
		{"東京電力", []string{"t3"}},  // comment
		{"ガス", []string{"t3"}},    // 2 chars in comment
		{"契約 電気", []string{"t3"}}, // multiple terms are ANDed
		{"ソファ 転居", []string{}},    // no task has both
		{`100%`, []string{}},      // LIKE wildcards are escaped
		{`"引用符"`, []string{}},     // FTS syntax is escaped
	} {
		t.Run(tc.query, func(t *testing.T) {
			got, err := s.ListTasks(ctx, TaskFilter{Query: tc.query})
			if err != nil {
				t.Fatal(err)
			}
			if !equal(gids(got), tc.want) {
				t.Fatalf("got %v, want %v", gids(got), tc.want)
			}
		})
	}
}

func TestSearchIndexFollowsUpdates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "テーブルを買う"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListTasks(ctx, TaskFilter{Query: "ソファ"}); len(got) != 0 {
		t.Fatalf("old name still indexed: %v", gids(got))
	}
	if got, _ := s.ListTasks(ctx, TaskFilter{Query: "テーブル"}); !equal(gids(got), []string{"t1"}) {
		t.Fatalf("new name not indexed: %v", gids(got))
	}
}

func TestGetTask(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	d, err := s.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if d.Notes != "リビング用" || d.Assignee.Name != "Me" || len(d.Subtasks) != 1 || d.Subtasks[0].GID != "t1a" {
		t.Fatalf("detail %+v", d)
	}
	if len(d.Memberships) != 1 || d.Memberships[0].ProjectName != "[家族]新居プロジェクト" || d.Memberships[0].SectionName != "家具" {
		t.Fatalf("memberships %+v", d.Memberships)
	}
	if !equal(d.Tags, []string{"買い物"}) {
		t.Fatalf("tags %v", d.Tags)
	}
	if d.DetailsFetchedAt != "" {
		t.Fatalf("details not fetched yet, got %q", d.DetailsFetchedAt)
	}
	d3, _ := s.GetTask(ctx, "t3")
	if len(d3.Stories) != 1 || d3.DetailsFetchedAt == "" {
		t.Fatalf("t3 stories %+v", d3)
	}
	if sub, _ := s.GetTask(ctx, "t1a"); sub.Parent == nil || sub.Parent.Name != "ソファを買う" {
		t.Fatalf("parent %+v", sub.Parent)
	}
	if _, err := s.GetTask(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveProject(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for arg, want := range map[string]string{"p2": "p2", "[家族]事務/雑務": "p2", "新居": "p1", "仕事": "p3"} {
		if got, err := s.ResolveProject(ctx, arg); err != nil || got != want {
			t.Errorf("ResolveProject(%q) = %q, %v", arg, got, err)
		}
	}
	if _, err := s.ResolveProject(ctx, "家族"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("ambiguous: %v", err)
	}
	if _, err := s.ResolveProject(ctx, "ない"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if ps, _ := s.ListProjects(ctx, true); len(ps) != 2 || len(ps[0].Sections)+len(ps[1].Sections) != 2 {
		t.Errorf("ListProjects %+v", ps)
	}
}

func TestInlineAssets(t *testing.T) {
	text := "見て https://app.asana.com/app/asana/-/get_asset?asset_id=111 と\n(https://app.asana.com/app/asana/-/get_asset?asset_id=222&size=large) 以上"
	if got := InlineAssets(text); len(got) != 2 || got[0] != "111" || got[1] != "222" {
		t.Fatalf("InlineAssets = %v", got)
	}
	got := ReplaceInlineAssets(text, func(gid string) string { return "[" + gid + "]" })
	if got != "見て [111] と\n([222]) 以上" {
		t.Fatalf("ReplaceInlineAssets = %q", got)
	}
	if InlineAssets("https://app.asana.com/0/123/456") != nil {
		t.Fatal("task links are not images")
	}
}
