package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// projectCLIEnv is writeCLIEnv with projects in two teams, a favorite, and p1's fields.
func projectCLIEnv(t *testing.T) string {
	t.Helper()
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	me := []asana.Ref{{GID: "me"}}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "p1", Name: "新居", Members: me, Team: &asana.Ref{GID: "tm1", Name: "家族"}, Owner: &asana.Ref{GID: "u2", Name: "Partner"},
			Notes: "年内に引っ越す", DueOn: "2026-12-31", Color: "dark-green", PermalinkURL: "https://app.asana.com/0/p1"},
		{GID: "p2", Name: "仕事", Members: me, Team: &asana.Ref{GID: "tm2", Name: "会社"}},
		{GID: "p3", Name: "趣味", Members: me, Team: &asana.Ref{GID: "tm2", Name: "会社"}},
	}, "me"))
	must(st.ReplaceFavoriteProjects(ctx, "w1", []asana.Ref{{GID: "p3"}}))
	must(st.SaveProjectOverview(ctx, "p1", &asana.TaskCounts{NumTasks: 4, NumCompletedTasks: 1}, store.BriefUnavailable, nil, time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)))
	return path
}

// #38: the list is under headings (favorites, then each team), and --team narrows it.
func TestProjectListByTeam(t *testing.T) {
	projectCLIEnv(t)
	out, err := runCLI(t, "project", "list")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"[★ お気に入り]", "趣味  (p3)", "[会社]", "仕事  (p2)", "[家族]", "新居  (p1)", "  - 家具  (s1)"}
	idx := 0
	for _, w := range want {
		i := strings.Index(out[idx:], w)
		if i < 0 {
			t.Fatalf("%q missing or out of order in:\n%s", w, out)
		}
		idx += i
	}
	out, err = runCLI(t, "project", "list", "--team", "会社")
	if err != nil || strings.Contains(out, "新居") || !strings.Contains(out, "仕事") || !strings.Contains(out, "趣味") {
		t.Fatalf("--team 会社: %v\n%s", err, out)
	}
	stdout, _, err := runCLISplit(t, "--json", "project", "list", "--team", "tm1")
	var ps []readmodel.ProjectView
	if err != nil || json.Unmarshal([]byte(stdout), &ps) != nil || len(ps) != 1 || ps[0].GID != "p1" || ps[0].TeamGID != "tm1" {
		t.Fatalf("--json --team tm1: %v %s", err, stdout)
	}
	if out, err := runCLI(t, "project", "list", "--team", "経理"); err == nil {
		t.Fatalf("unknown team accepted:\n%s", out)
	}
}

// project teams lists the teams; project create picks the only one, or asks for --team.
func TestProjectTeamsAndCreate(t *testing.T) {
	projectCLIEnv(t)
	out, err := runCLI(t, "project", "teams")
	if err != nil || !strings.Contains(out, "会社  (tm2)") || !strings.Contains(out, "家族  (tm1)") {
		t.Fatalf("teams: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "project", "create", "引っ越し", "--dry-run"); err == nil || !strings.Contains(err.Error(), "--team") {
		t.Fatalf("two teams and no --team: %v\n%s", err, out)
	}
	stdout, _, err := runCLISplit(t, "--json", "project", "create", "引っ越し", "--team", "家族", "--dry-run")
	if err != nil || !strings.Contains(stdout, `"team": "tm1"`) {
		t.Fatalf("--team 家族: %v\n%s", err, stdout)
	}
}

// project show reads the cache offline: fields, progress, description; no brief when
// Asana does not give it.
func TestProjectShowOffline(t *testing.T) {
	projectCLIEnv(t)
	out, err := runCLI(t, "project", "show", "新居", "--offline")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"新居  (p1)", "チーム: 家族", "オーナー: Partner", "期間: 〜 2026-12-31", "色: dark-green",
		"進み具合: 1 / 4 件完了（25%）", "年内に引っ越す", "https://app.asana.com/0/p1"} {
		if !strings.Contains(out, want) {
			t.Errorf("show lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "ブリーフ") {
		t.Errorf("an unavailable brief is shown:\n%s", out)
	}
	stdout, _, err := runCLISplit(t, "--json", "project", "show", "新居", "--offline")
	var o readmodel.ProjectOverview
	if err != nil || json.Unmarshal([]byte(stdout), &o) != nil || o.Counts == nil || o.Counts.Tasks != 4 || o.Owner.GID != "u2" {
		t.Fatalf("show --json: %v %s", err, stdout)
	}
}

// Invalid edits are refused before anything is queued, naming the flag.
func TestProjectEditRefuses(t *testing.T) {
	path := projectCLIEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"project", "edit", "新居"}, "--notes"},
		{[]string{"project", "edit", "新居", "--color", "rainbow"}, "色は"},
		{[]string{"project", "edit", "新居", "--start", "2027-01-31"}, "開始日"},
		{[]string{"project", "edit", "仕事", "--start", "today"}, "期日も必要"},
		{[]string{"project", "edit", "新居", "--due", "そのうち"}, "--due"},
		{[]string{"project", "edit", "新居", "--owner", "誰か"}, "--owner"},
		{[]string{"project", "edit", "ない", "--color", "none"}, "ない"},
	} {
		out, err := runCLI(t, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want %q\n%s", tc.args, err, tc.want, out)
		}
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued: %v", opKinds(ops))
	}
	// "" clears a field.
	if out, err := runCLI(t, "project", "edit", "新居", "--due", "", "--owner", ""); err != nil {
		t.Fatalf("clear: %v\n%s", err, out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Payload["due_on"] != "" || ops[0].Payload["owner"] != "" {
		t.Fatalf("clear queued %+v", ops)
	}
}
