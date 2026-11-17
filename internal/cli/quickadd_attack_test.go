package cli

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// A token with control characters is not printed raw in the text output; the JSON escapes it.
func TestCreateQuickAttackControlCharacters(t *testing.T) {
	writeCLIEnv(t)
	out, err := runCLI(t, "task", "create", "--quick", "a", "#\x1b[31mnope", "@\x1b]0;x\x07")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("control characters in the output: %q", out)
	}
	writeCLIEnv(t)
	stdout, _, err := runCLISplit(t, "--json", "task", "create", "--quick", "a", "#\x1b[31mnope")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(stdout, "\x1b\x07") || !json.Valid([]byte(stdout)) {
		t.Errorf("json: %q", stdout)
	}
}

// Only tokens: the first word is the name. A line of only blanks is an error, nothing queued.
func TestCreateQuickAttackBoundaries(t *testing.T) {
	path := writeCLIEnv(t)
	if out, err := runCLI(t, "task", "create", "--quick", "@me", "明日", "#急ぎ"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	d, kinds := createdTask(t, path)
	if d.Name != "@me" || d.DueOn == "" || !slices.Equal(tagNames(d), []string{"急ぎ"}) || !slices.Equal(kinds, []string{store.OpCreateTask, store.OpAddTag}) {
		t.Errorf("%+v %q", d, kinds)
	}

	path = writeCLIEnv(t)
	if out, err := runCLI(t, "task", "create", "--quick", "  ", "　"); err == nil {
		t.Errorf("a blank name should fail:\n%s", out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("nothing should be queued: %+v", ops)
	}
}

// --quick with --parent (a subtask) and --project reads the tokens too, writes once.
func TestCreateQuickAttackWithParentAndProject(t *testing.T) {
	path := writeCLIEnv(t)
	if out, err := runCLI(t, "task", "create", "--quick", "--project", "新居", "--section", "家具", "棚", "@partner", "#急ぎ"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	d, kinds := createdTask(t, path)
	if d.Name != "棚" || d.Assignee == nil || d.Assignee.GID != "u2" || !slices.Equal(tagNames(d), []string{"急ぎ"}) {
		t.Errorf("%+v", d)
	}
	if !slices.Equal(kinds, []string{store.OpCreateTask, store.OpAddTag}) {
		t.Errorf("ops %q", kinds)
	}
}

// Same-valued flag and token still conflict (not silently merged), before anything is written.
func TestCreateQuickAttackSameValueConflicts(t *testing.T) {
	path := writeCLIEnv(t)
	if out, err := runCLI(t, "task", "create", "--quick", "--assignee", "me", "a", "@me"); err == nil {
		t.Errorf("should conflict:\n%s", out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("queued: %+v", ops)
	}
}
