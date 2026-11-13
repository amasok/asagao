package cli

import (
	"reflect"
	"testing"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/store"
)

func TestMembershipLabel(t *testing.T) {
	for _, tc := range []struct {
		name string
		ms   []store.MembershipView
		want string
	}{
		{"none", nil, ""},
		{"project and section", []store.MembershipView{{ProjectName: "新居", SectionName: "家具"}}, "新居 / 家具"},
		{"no section", []store.MembershipView{{ProjectName: "新居"}}, "新居"},
		{"placeholder sections are hidden", []store.MembershipView{
			{ProjectName: "A", SectionName: "(no section)"}, {ProjectName: "B", SectionName: "Untitled section"}}, "A, B"},
		{"several projects", []store.MembershipView{
			{ProjectName: "新居", SectionName: "家具"}, {ProjectName: "家計"}}, "新居 / 家具, 家計"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := membershipLabel(tc.ms); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The filter logic itself is tested in store (TestBuildFilter); this checks only how
// the CLI flags map onto it.
func TestListFlagsSpec(t *testing.T) {
	open := false
	for _, tc := range []struct {
		name string
		args []string
		want store.FilterSpec
	}{
		{"defaults", nil, store.FilterSpec{DefaultAssignee: "me", DefaultCompleted: &open}},
		{"explicit assignee", []string{"--assignee", "Partner"}, store.FilterSpec{Assignee: "Partner", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"empty assignee means everyone", []string{"--assignee="}, store.FilterSpec{Assignee: "any", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"--all wins over --completed", []string{"--completed", "--all"}, store.FilterSpec{Completion: "all", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"--completed", []string{"--completed"}, store.FilterSpec{Completion: "completed", DefaultAssignee: "me", DefaultCompleted: &open}},
		{"the rest pass through", []string{"--project", "新居", "--section", "家具", "--tag", "急ぎ", "--due-before", "+7d",
			"--due-after", "today", "--overdue", "--modified-within", "3", "--limit", "5"},
			store.FilterSpec{DefaultAssignee: "me", DefaultCompleted: &open, Project: "新居", Section: "家具", Tag: "急ぎ",
				DueBefore: "+7d", DueAfter: "today", Overdue: true, ModifiedWithinDays: 3, Limit: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f listFlags
			cmd := &cobra.Command{Use: "x"}
			f.register(cmd, "me")
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatal(err)
			}
			if got := f.spec(cmd, "me", &open); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}
