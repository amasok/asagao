package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

// fieldsCLIEnv makes a private store (offline: no token) with task t1 in p1 and an enum
// field 優先度 (高/中).
func fieldsCLIEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("ASANA_TOKEN", "")
	t.Setenv("ASANA_CLIENT_ID", "")
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(config.DataDir(), "store.db")
	st := createStoreAt(t, path)
	ctx := context.Background()
	on := true
	if err := st.ReplaceProjectCustomFields(ctx, "p1", []asana.CustomFieldSetting{{GID: "s1", IsImportant: true,
		CustomField: asana.CustomField{GID: "cf1", Name: "優先度", ResourceSubtype: "enum",
			EnumOptions: []asana.EnumOption{{GID: "o1", Name: "高", Enabled: &on}, {GID: "o2", Name: "中", Enabled: &on}}}}}); err != nil {
		t.Fatal(err)
	}
	p1 := []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}
	cf := []asana.CustomField{{GID: "cf1", Name: "優先度", ResourceSubtype: "enum"}}
	if err := st.UpsertTasks(ctx, []asana.Task{
		{GID: "t1", Name: "ソファを買う", Assignee: &asana.Ref{GID: "me"}, Workspace: &asana.Ref{GID: "w1"}, Memberships: p1, CustomFields: cf},
		{GID: "t2", Name: "カーテンを買う", Assignee: &asana.Ref{GID: "me"}, Workspace: &asana.Ref{GID: "w1"}, Memberships: p1, CustomFields: cf},
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	return path
}

func cliOps(t *testing.T, path string) []store.Op {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ops, _ := st.ListOps(context.Background())
	return ops
}

// task field sets a value offline (queued), refuses an unknown option without queueing
// anything, and task list --field filters on it.
func TestTaskFieldCommand(t *testing.T) {
	path := fieldsCLIEnv(t)
	if out, err := runCLI(t, "task", "field", "t1", "優先度", "最高"); err == nil {
		t.Fatalf("an unknown option should be refused\n%s", out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("a refused value must queue nothing: %+v", ops)
	}
	if out, err := runCLI(t, "task", "field", "t1", "優先度", "高"); err != nil {
		t.Fatalf("task field: %v\n%s", err, out)
	}
	ops := cliOps(t, path)
	if len(ops) != 1 || ops[0].Kind != store.OpSetCustomField || ops[0].State != store.OpPending {
		t.Fatalf("ops = %+v", ops)
	}
	out, err := runCLI(t, "task", "list", "--field", "優先度=高")
	if err != nil || !strings.Contains(out, "ソファを買う") || strings.Contains(out, "カーテンを買う") {
		t.Fatalf("task list --field 優先度=高: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "task", "show", "t1"); err != nil || !strings.Contains(out, "優先度") || !strings.Contains(out, "高") {
		t.Fatalf("task show: %v\n%s", err, out)
	}
	// Clearing it again cancels the queued change (back to the value Asana has).
	if out, err := runCLI(t, "task", "field", "t1", "優先度", "--clear"); err != nil {
		t.Fatalf("--clear: %v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("setting the field back should leave nothing to send: %+v", ops)
	}
}

// task depend queues links offline; a task that does not exist is refused.
func TestTaskDependCommand(t *testing.T) {
	path := fieldsCLIEnv(t)
	if out, err := runCLI(t, "task", "depend", "t1", "--add", "no-such-task"); err == nil {
		t.Fatalf("an unknown task should be refused\n%s", out)
	}
	if out, err := runCLI(t, "task", "depend", "t1", "--add", "t1"); err == nil {
		t.Fatalf("a task cannot wait for itself\n%s", out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("refused links must queue nothing: %+v", ops)
	}
	if out, err := runCLI(t, "task", "depend", "t1", "--add", "t2"); err != nil {
		t.Fatalf("depend: %v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 1 {
		t.Fatalf("ops = %+v", ops)
	}
	if out, err := runCLI(t, "task", "show", "t1"); err != nil || !strings.Contains(out, "カーテンを買う") {
		t.Fatalf("task show should list what it waits for: %v\n%s", err, out)
	}
}
