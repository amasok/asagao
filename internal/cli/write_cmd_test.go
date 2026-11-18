package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
)

// writeCLIEnv is fieldsCLIEnv (offline: no token; tasks t1 and t2 in 新居 / 家具) with a
// second section 完了 and a tag 急ぎ.
func writeCLIEnv(t *testing.T) string {
	t.Helper()
	path := fieldsCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.ReplaceSections(ctx, "p1", []asana.Section{{GID: "s1", Name: "家具"}, {GID: "s2", Name: "完了"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTags(ctx, []asana.Tag{{GID: "g1", Name: "急ぎ"}}); err != nil {
		t.Fatal(err)
	}
	return path
}

func opKinds(ops []store.Op) []string {
	kinds := make([]string, len(ops))
	for i, op := range ops {
		kinds[i] = op.Kind + ":" + op.TaskGID
	}
	return kinds
}

// Offline, each write command changes the local copy, queues the change for the next sync
// and says it was not sent.
func TestWriteCommandsOfflineQueue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		kind  string // the queued op
		task  string // its task (the section, for a section)
		check func(t *testing.T, st *store.Store)
	}{
		{"complete", []string{"task", "complete", "t1"}, store.OpUpdateTask, "t1", func(t *testing.T, st *store.Store) {
			if d, _ := st.GetTask(context.Background(), "t1"); d == nil || !d.Completed {
				t.Errorf("t1 not completed locally: %+v", d)
			}
		}},
		{"assign by name", []string{"task", "assign", "t1", "Partner"}, store.OpUpdateTask, "t1", func(t *testing.T, st *store.Store) {
			if d, _ := st.GetTask(context.Background(), "t1"); d == nil || d.Assignee == nil || d.Assignee.GID != "u2" {
				t.Errorf("t1 assignee: %+v", d)
			}
		}},
		{"unassign", []string{"task", "assign", "t1", "none"}, store.OpUpdateTask, "t1", func(t *testing.T, st *store.Store) {
			if d, _ := st.GetTask(context.Background(), "t1"); d == nil || d.Assignee != nil {
				t.Errorf("t1 should be unassigned: %+v", d)
			}
		}},
		{"move", []string{"task", "move", "t1", "--section", "完了"}, store.OpMoveTask, "t1", nil},
		{"comment", []string{"comment", "add", "t1", "**明日**", "届く"}, store.OpAddComment, "t1", nil},
		{"tag", []string{"task", "tag", "t1", "--add", "急ぎ"}, store.OpAddTag, "t1", nil},
		{"section rename", []string{"section", "rename", "家具", "大型家具", "--project", "新居"}, store.OpRenameSection, "s1", nil},
		{"section move", []string{"section", "move", "完了", "--project", "新居", "--before", "家具"}, store.OpInsertSection, "s2", nil},
		{"section rm", []string{"section", "rm", "完了", "--project", "新居", "--yes"}, store.OpDeleteSection, "s2", nil},
		{"tag rename", []string{"tag", "rename", "急ぎ", "至急"}, store.OpUpdateTag, "g1", nil},
		{"tag color", []string{"tag", "color", "急ぎ", "Dark-Red"}, store.OpUpdateTag, "g1", func(t *testing.T, st *store.Store) {
			if g, _ := st.GetTag(context.Background(), "g1"); g.Color != "dark-red" {
				t.Errorf("g1 = %+v", g)
			}
		}},
		{"tag rm", []string{"tag", "rm", "急ぎ", "--yes"}, store.OpDeleteTag, "g1", nil},
		{"project edit", []string{"project", "edit", "新居", "--owner", "Partner", "--start", "2026-12-01", "--due", "2026-12-31",
			"--color", "dark-red", "--notes", "**年内**に引っ越す"}, store.OpUpdateProject, "p1", func(t *testing.T, st *store.Store) {
			o, _ := st.ProjectOverview(context.Background(), "p1")
			if o == nil || o.Owner == nil || o.Owner.GID != "u2" || o.StartOn != "2026-12-01" || o.DueOn != "2026-12-31" ||
				o.Color != "dark-red" || o.Notes != "年内に引っ越す" || !strings.Contains(o.HTMLNotes, "<strong>年内</strong>") {
				t.Errorf("p1 locally: %+v", o)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeCLIEnv(t)
			out, err := runCLI(t, tc.args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.Contains(out, "未送信") {
				t.Errorf("should say it is not sent yet:\n%s", out)
			}
			ops := cliOps(t, path)
			if len(ops) != 1 || ops[0].Kind != tc.kind || ops[0].TaskGID != tc.task || ops[0].State != store.OpPending {
				t.Fatalf("ops = %v, want %s:%s", opKinds(ops), tc.kind, tc.task)
			}
			if tc.check != nil {
				st, err := store.Open(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				tc.check(t, st)
			}
		})
	}
}

// The local changes show in the read commands.
func TestWriteCommandsShowLocally(t *testing.T) {
	writeCLIEnv(t)
	for _, args := range [][]string{
		{"task", "move", "t1", "--section", "完了"},
		{"comment", "add", "t1", "明日届く"},
		{"task", "tag", "t1", "--add", "急ぎ"},
		{"task", "complete", "t2"},
	} {
		if out, err := runCLI(t, args...); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	out, err := runCLI(t, "task", "show", "t1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"完了", "明日届く", "急ぎ"} {
		if !strings.Contains(out, want) {
			t.Errorf("task show lacks %q:\n%s", want, out)
		}
	}
	// t2 is completed: not in the open task list
	out, err = runCLI(t, "task", "list", "--project", "新居")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "カーテンを買う") {
		t.Errorf("a completed task is listed as open:\n%s", out)
	}
	// reopen brings it back
	if out, err := runCLI(t, "task", "reopen", "t2"); err != nil {
		t.Fatalf("reopen: %v\n%s", err, out)
	}
	if out, _ := runCLI(t, "task", "list", "--project", "新居"); !strings.Contains(out, "カーテンを買う") {
		t.Errorf("reopened task not listed:\n%s", out)
	}
	if out, _ := runCLI(t, "tag", "list"); !strings.Contains(out, "#急ぎ") || !strings.Contains(out, "未完了 1 / 全 1") {
		t.Errorf("tag list:\n%s", out)
	}
	if out, _ := runCLI(t, "outbox", "list"); !strings.Contains(out, "明日届く") {
		t.Errorf("outbox list should show the queued comment:\n%s", out)
	}
}

// A write command refuses what it cannot resolve and queues nothing.
func TestWriteCommandsRefuseUnknown(t *testing.T) {
	for _, args := range [][]string{
		{"task", "assign", "t1", "誰か"},
		{"task", "move", "t1", "--section", "ない"},
		{"task", "move", "t1"}, // --section is required
		{"task", "complete", "no-such-task"},
		{"task", "complete"},
		{"comment", "add", "no-such-task", "やあ"},
		{"comment", "add", "t1"},
		{"section", "rename", "ない", "新", "--project", "新居"},
		{"section", "rename", "家具", "新"}, // --project is required
		{"section", "create", "新"},       // --project is required
		{"section", "create", "新", "--project", "新居", "--after", "ない"},
		{"section", "create", "新", "--project", "新居", "--before", "家具", "--after", "完了"},
		{"section", "move", "完了", "--project", "新居"}, // where to?
		{"section", "move", "完了", "--project", "新居", "--before", "完了"},
		{"section", "rm", "完了", "--project", "新居"}, // --yes is required
		{"tag", "rm", "急ぎ"}, // --yes is required
		{"tag", "color", "急ぎ", "green"},
		{"tag", "rename", "ないタグ", "x"},
		{"task", "tag", "t1", "--remove", "ないタグ"},
		{"task", "create", "x", "--due", "そのうち"},
		{"task", "create", "x", "--project", "ない"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := writeCLIEnv(t)
			if out, err := runCLI(t, args...); err == nil {
				t.Fatalf("should fail\n%s", out)
			}
			if ops := cliOps(t, path); len(ops) != 0 {
				t.Fatalf("queued %v", opKinds(ops))
			}
		})
	}
}

// --json of a write command is one JSON object on the output (nothing else there): not
// sent, why, and the task.
func TestWriteCommandJSON(t *testing.T) {
	writeCLIEnv(t)
	stdout, _, err := runCLISplit(t, "--json", "task", "complete", "t1", "t2")
	if err != nil {
		t.Fatal(err)
	}
	var r writeResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("json %q: %v", stdout, err)
	}
	if r.Sent || r.TaskGID != "t1,t2" || r.Error == "" {
		t.Fatalf("result %+v", r)
	}

	stdout, _, err = runCLISplit(t, "--json", "task", "create", "棚を組む", "--project", "新居")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("json %q: %v", stdout, err)
	}
	if r.Sent || !store.IsLocalGID(r.TaskGID) {
		t.Fatalf("an unsent new task keeps its local gid: %+v", r)
	}
}

// Project changes are online only: offline they fail and change nothing; listing members
// works from the store.
func TestProjectCommandsOffline(t *testing.T) {
	writeCLIEnv(t)
	for _, args := range [][]string{
		{"project", "create", "引っ越し"},
		{"project", "archive", "新居"},
		{"project", "members", "新居", "--add", "Partner"},
	} {
		out, err := runCLI(t, args...)
		if err == nil || !strings.Contains(err.Error(), "接続できません") {
			t.Errorf("%v: should fail as offline: %v\n%s", args, err, out)
		}
	}
	if out, err := runCLI(t, "project", "archive", "ない"); err == nil {
		t.Errorf("an unknown project should be refused\n%s", out)
	}
	out, err := runCLI(t, "project", "members", "新居")
	if err != nil || strings.TrimSpace(out) != "Me" {
		t.Fatalf("members: %v\n%q", err, out)
	}
	stdout, _, err := runCLISplit(t, "--json", "project", "members", "新居")
	if err != nil {
		t.Fatal(err)
	}
	var members []store.UserRef
	if err := json.Unmarshal([]byte(stdout), &members); err != nil || len(members) != 1 || members[0].GID != "me" {
		t.Fatalf("members json %q: %v", stdout, err)
	}
}

// sync needs a login; sync status reports the store and what is waiting to be sent.
func TestSyncCommandOffline(t *testing.T) {
	writeCLIEnv(t)
	if out, err := runCLI(t, "sync"); err == nil {
		t.Fatalf("sync without a login should fail\n%s", out)
	}
	if out, err := runCLI(t, "comment", "add", "t1", "メモ"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	out, err := runCLI(t, "sync", "status")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"最終同期: 未同期", "タスク 2（未完了 2）", "未送信 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestFormatDue(t *testing.T) {
	year := time.Now().Year()
	for _, tc := range []struct{ due, want string }{
		{"", ""},
		{"not-a-date", "not-a-date"},
		{time.Date(year, 3, 1, 0, 0, 0, 0, time.UTC).Format(time.DateOnly), "03/01(" + weekdays[time.Date(year, 3, 1, 0, 0, 0, 0, time.UTC).Weekday()] + ")"},
		{"2020-01-05", "2020/01/05(日)"},
	} {
		if got := formatDue(store.TaskSummary{DueOn: tc.due}); got != tc.want {
			t.Errorf("formatDue(%q) = %q, want %q", tc.due, got, tc.want)
		}
	}
}
