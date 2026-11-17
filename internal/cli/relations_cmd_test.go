package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/store"
)

// relationsCLIEnv is writeCLIEnv with t1 tagged 急ぎ and followed by Partner (u2), and a
// second project 仕事 (p2).
func relationsCLIEnv(t *testing.T) string {
	t.Helper()
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p2", Name: "仕事", Members: []asana.Ref{{GID: "me"}}}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceSections(ctx, "p2", []asana.Section{{GID: "s9", Name: "未着手"}}); err != nil {
		t.Fatal(err)
	}
	p1 := []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", Assignee: &asana.Ref{GID: "me"},
		Workspace: &asana.Ref{GID: "w1"}, Memberships: p1, Tags: []asana.Ref{{GID: "g1", Name: "急ぎ"}},
		Followers: []asana.Ref{{GID: "u2", Name: "Partner"}}}}); err != nil {
		t.Fatal(err)
	}
	return path
}

// The relation commands (tag, follow, project, depend) queue one change for each name,
// on the right task: a dependent's change is queued on the task that waits.
func TestRelationCommandsQueue(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string // kind:task of the queued ops
	}{
		{"tag remove", []string{"task", "tag", "t1", "--remove", "急ぎ"}, []string{store.OpRemoveTag + ":t1"}},
		{"tag add and remove", []string{"task", "tag", "t1", "--add", "新しい", "--remove", "急ぎ"}, []string{store.OpAddTag + ":t1", store.OpRemoveTag + ":t1"}},
		{"follow add", []string{"task", "follow", "t1", "--add", "me"}, []string{store.OpAddFollower + ":t1"}},
		{"follow remove", []string{"task", "follow", "t1", "--remove", "Partner"}, []string{store.OpRemoveFollower + ":t1"}},
		{"follow both", []string{"task", "follow", "t1", "--add", "me", "--remove", "u2"}, []string{store.OpAddFollower + ":t1", store.OpRemoveFollower + ":t1"}},
		{"project add", []string{"task", "project", "t1", "--add", "仕事"}, []string{store.OpAddProject + ":t1"}},
		{"project remove", []string{"task", "project", "t1", "--remove", "新居"}, []string{store.OpRemoveProject + ":t1"}},
		{"depend", []string{"task", "depend", "t1", "--add", "t2"}, []string{store.OpAddDependency + ":t1"}},
		{"depend dependent", []string{"task", "depend", "t1", "--add-dependent", "t2"}, []string{store.OpAddDependency + ":t2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := relationsCLIEnv(t)
			out, err := runCLI(t, tc.args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if !strings.Contains(out, "未送信") {
				t.Errorf("should say it is not sent yet:\n%s", out)
			}
			if got := opKinds(cliOps(t, path)); !slices.Equal(got, tc.want) {
				t.Errorf("ops = %v, want %v", got, tc.want)
			}
		})
	}
}

// A name that does not resolve is reported after the flag it came from, and nothing is
// queued, not even the names before it that did resolve.
func TestRelationCommandsNameTheFlag(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		prefix string
	}{
		{[]string{"task", "follow", "t1", "--add", "me", "--add", "誰か"}, "--add: "},
		{[]string{"task", "follow", "t1", "--add", "me", "--remove", "誰か"}, "--remove: "},
		{[]string{"task", "project", "t1", "--add", "ない"}, "--add: "},
		{[]string{"task", "project", "t1", "--add", "仕事", "--remove", "ない"}, "--remove: "},
		{[]string{"task", "depend", "t1", "--add", "nope"}, "--add: "},
		{[]string{"task", "depend", "t1", "--add", "t2", "--remove", "nope"}, "--remove: "},
		{[]string{"task", "depend", "t1", "--add", "t2", "--add-dependent", "nope"}, "--add-dependent: "},
		{[]string{"task", "depend", "t1", "--add", "t2", "--remove-dependent", "nope"}, "--remove-dependent: "},
		{[]string{"task", "depend", "t1"}, "--add / --remove / --add-dependent / --remove-dependent のどれかを指定してください"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			path := relationsCLIEnv(t)
			out, err := runCLI(t, tc.args...)
			if err == nil {
				t.Fatalf("should fail\n%s", out)
			}
			if !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Errorf("err = %q, want it to start with %q", err, tc.prefix)
			}
			if ops := cliOps(t, path); len(ops) != 0 {
				t.Errorf("queued %v", opKinds(ops))
			}
		})
	}
}

// task duplicate queues a copy (a local task), and task attach queues the file on the
// task, copied into the cache; a file that does not exist queues nothing.
func TestDuplicateAndAttachCommandsQueue(t *testing.T) {
	path := relationsCLIEnv(t)
	if out, err := runCLI(t, "task", "attach", "t1", filepath.Join(t.TempDir(), "ない.pdf")); err == nil {
		t.Fatalf("a missing file should be refused\n%s", out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued %v", opKinds(ops))
	}
	out, err := runCLI(t, "task", "duplicate", "t1", "--name", "ソファを買う (2)")
	if err != nil || !strings.Contains(out, "未送信") {
		t.Fatalf("duplicate: %v\n%s", err, out)
	}
	file := filepath.Join(t.TempDir(), "見積.pdf")
	if err := os.WriteFile(file, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "task", "attach", "t1", file); err != nil || !strings.Contains(out, "見積.pdf") {
		t.Fatalf("attach: %v\n%s", err, out)
	}
	ops := cliOps(t, path)
	if len(ops) != 2 || ops[0].Kind != store.OpDuplicateTask || !store.IsLocalGID(ops[0].TaskGID) ||
		ops[1].Kind != store.OpUploadAttachment || ops[1].TaskGID != "t1" {
		t.Fatalf("ops = %v", opKinds(ops))
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c, err := st.GetTask(context.Background(), ops[0].TaskGID)
	if err != nil || c.Name != "ソファを買う (2)" {
		t.Errorf("copy = %+v, %v", c, err)
	}
	d, _ := st.GetTask(context.Background(), "t1")
	if len(d.Attachments) != 1 || !strings.HasPrefix(d.Attachments[0].LocalPath, filepath.Join(config.CacheDir(), "attachments")) {
		t.Errorf("attachments = %+v, want one copied under the cache", d.Attachments)
	}
}
