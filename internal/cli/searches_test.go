package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/readmodel"
)

// searchesCLIEnv is a private data and config folder with a store of four tasks: two of
// mine due this week and next month, two about 請求書 (one completed, someone else's).
func searchesCLIEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(config.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	st := createStoreAt(t, filepath.Join(config.DataDir(), "store.db"))
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format(time.DateOnly) }
	in := []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}
	me := &asana.Ref{GID: "me", Name: "Me"}
	if err := st.UpsertTasks(context.Background(), []asana.Task{
		{GID: "t1", Name: "今週の仕事", DueOn: day(3), Assignee: me, Memberships: in},
		{GID: "t2", Name: "来月の仕事", DueOn: day(30), Assignee: me, Memberships: in},
		{GID: "t3", Name: "請求書を送る", Completed: true, Assignee: &asana.Ref{GID: "u2", Name: "Partner"}, Memberships: in},
		{GID: "t4", Name: "請求書の確認", Assignee: me, Memberships: in},
	}); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
}

func TestSearchesCommands(t *testing.T) {
	searchesCLIEnv(t)
	must := func(args ...string) string {
		t.Helper()
		out, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return out
	}

	must("searches", "save", "今週", "--due-before", "+7d")
	b, err := os.ReadFile(config.SearchesFilePath())
	if err != nil || !strings.Contains(string(b), "+7d") {
		t.Fatalf("the relative date is saved as written: %v\n%s", err, b)
	}
	// It is read on the day it runs: due in 3 days, not in 30.
	if out := must("searches", "run", "今週"); !strings.Contains(out, "今週の仕事") || strings.Contains(out, "来月の仕事") || strings.Contains(out, "請求書") {
		t.Fatalf("run 今週:\n%s", out)
	}

	// A taken name needs --force.
	if out, err := runCLI(t, "searches", "save", "今週", "--due-before", "+60d"); err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("taken name: %v\n%s", err, out)
	}
	must("searches", "save", "今週", "--due-before", "+60d", "--force")
	if out := must("searches", "run", "今週"); !strings.Contains(out, "来月の仕事") {
		t.Fatalf("overwritten search:\n%s", out)
	}

	// With a query it runs like `search`: everyone's, completed too.
	must("searches", "save", "請求", "--query", "請求書")
	if out := must("searches", "run", "請求"); !strings.Contains(out, "請求書を送る") || !strings.Contains(out, "請求書の確認") || strings.Contains(out, "仕事") {
		t.Fatalf("run 請求:\n%s", out)
	}
	must("searches", "save", "請求の未完了", "--query", "請求書", "--open", "--project", "新居")
	if out := must("searches", "run", "請求の未完了"); strings.Contains(out, "請求書を送る") || !strings.Contains(out, "請求書の確認") {
		t.Fatalf("run 請求の未完了:\n%s", out)
	}

	out := must("searches", "list")
	for _, w := range []string{"今週\tdue_before=+60d", "請求\tquery=請求書", "請求の未完了\tquery=請求書 project=新居 completion=open"} {
		if !strings.Contains(out, w) {
			t.Errorf("list does not show %q:\n%s", w, out)
		}
	}
	js, _, err := runCLISplit(t, "searches", "list", "--json")
	var got []readmodel.SavedSearch
	if err != nil || json.Unmarshal([]byte(js), &got) != nil || len(got) != 3 || got[0].DueBefore != "+60d" {
		t.Fatalf("list --json: %v\n%s", err, js)
	}

	must("searches", "rm", "請求")
	if out := must("searches", "list"); strings.Contains(out, "請求\t") {
		t.Fatalf("removed search listed:\n%s", out)
	}
	if out, err := runCLI(t, "searches", "rm", "請求"); err == nil {
		t.Fatalf("removing it twice: %s", out)
	}
	if out, err := runCLI(t, "searches", "run", "ない"); err == nil {
		t.Fatalf("running an unknown search: %s", out)
	}
}

func TestSearchesSaveRefusesBadConditions(t *testing.T) {
	searchesCLIEnv(t)
	for _, args := range [][]string{
		{"searches", "save", "x", "--open", "--all"},
		{"searches", "save", "x", "--due-before", "+7x"},
		{"searches", "save", "x", "--section", "家具"},
		{"searches", "save", " "},
	} {
		if out, err := runCLI(t, args...); err == nil {
			t.Errorf("%v saved:\n%s", args, out)
		}
	}
	if _, err := os.Stat(config.SearchesFilePath()); !os.IsNotExist(err) {
		t.Fatalf("a refused search wrote the file: %v", err)
	}
	// What the cache resolves (the project's name) is checked when the search runs.
	if out, err := runCLI(t, "searches", "save", "y", "--project", "ない"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if out, err := runCLI(t, "searches", "run", "y"); err == nil || !strings.Contains(err.Error(), `"y"`) {
		t.Fatalf("run with an unknown project: %v\n%s", err, out)
	}
}

// A hand-edited searches.toml with control characters in a name or an unknown key is an
// error that does not show the characters, and saving does not rewrite the file.
func TestSearchesHandEditedFile(t *testing.T) {
	searchesCLIEnv(t)
	path := config.SearchesFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		"[[search]]\nname = \"a\\u001b]0;evil\\u0007\"\n",
		"[[search]]\nname = \"a\"\ndue = \"today\"\n",
	} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"searches", "list"}, {"searches", "save", "b", "--force"}, {"searches", "rm", "a"}} {
			out, err := runCLI(t, args...)
			if err == nil {
				t.Fatalf("%v with %q: no error\n%s", args, body, out)
			}
			assertNoControls(t, err.Error())
		}
		if b, _ := os.ReadFile(path); string(b) != body {
			t.Fatalf("the file was rewritten:\n%s", b)
		}
	}
}
