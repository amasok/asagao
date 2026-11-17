package mcpserver

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/readmodel"
)

func searchFile(t *testing.T, ss ...readmodel.SavedSearch) config.SearchFile {
	t.Helper()
	f := config.SearchFile{Path: filepath.Join(t.TempDir(), "searches.toml")}
	if err := f.SaveSearches(ss); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSavedSearchTools(t *testing.T) {
	f := searchFile(t,
		readmodel.SavedSearch{Name: "今週", DueBefore: "+7d"},
		readmodel.SavedSearch{Name: "手続き", Project: "新居", Section: "手続き"},
		readmodel.SavedSearch{Name: "電気", Query: "電気"},
	)
	cs := connect(t, &Server{Store: seed(t), Searches: f})

	var list SavedSearchesOutput
	if res := call(t, cs, "list_saved_searches", nil, &list); res.IsError {
		t.Fatal(toolError(res))
	}
	if len(list.Searches) != 3 || list.Searches[0].Name != "今週" || list.Searches[0].DueBefore != "+7d" {
		t.Fatalf("list: %+v", list)
	}

	for _, tc := range []struct {
		name string
		want []string
	}{
		// +7d from testNow (11-13): ソファ (11-20) and the overdue 転居届 (11-10), mine only
		{"今週", []string{"ソファを買う", "転居届を出す"}},
		// a project: everyone's
		{"手続き", []string{"転居届を出す", "電気の契約"}},
		// a query: everyone's
		{"電気", []string{"電気の契約"}},
	} {
		var out RunSavedSearchOutput
		if res := call(t, cs, "run_saved_search", map[string]any{"name": tc.name}, &out); res.IsError {
			t.Fatalf("%s: %s", tc.name, toolError(res))
		}
		got := names(out.Tasks)
		slices.Sort(got)
		if !slices.Equal(got, tc.want) || out.Search.Name != tc.name || out.Count != len(tc.want) {
			t.Errorf("%s: %v, want %v (%+v)", tc.name, got, tc.want, out.Search)
		}
	}
	var out RunSavedSearchOutput
	if res := call(t, cs, "run_saved_search", map[string]any{"name": "今週", "limit": 1}, &out); res.IsError || out.Count != 1 {
		t.Fatalf("limit: %s %+v", toolError(res), out)
	}
	if res := call(t, cs, "run_saved_search", map[string]any{"name": "ない"}, nil); !res.IsError {
		t.Fatal("an unknown search ran")
	}
	if res := call(t, cs, "run_saved_search", map[string]any{"name": ""}, nil); !res.IsError {
		t.Fatal("an empty name ran")
	}
}

// Without a file, or with no searches server, the list is empty; a broken file is an
// error whose text has no control characters; the tools never write the file.
func TestSavedSearchToolsFile(t *testing.T) {
	var list SavedSearchesOutput
	cs := connect(t, &Server{Store: seed(t)})
	if res := call(t, cs, "list_saved_searches", nil, &list); res.IsError || list.Searches == nil || len(list.Searches) != 0 {
		t.Fatalf("no file: %s %+v", toolError(res), list)
	}

	path := filepath.Join(t.TempDir(), "searches.toml")
	body := "[[search]]\nname = \"a\\u001b]0;x\\u0007\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cs = connect(t, &Server{Store: seed(t), Searches: config.SearchFile{Path: path}})
	for _, args := range []map[string]any{nil, {"name": "a"}} {
		tool := "list_saved_searches"
		if args != nil {
			tool = "run_saved_search"
		}
		res := call(t, cs, tool, args, nil)
		if !res.IsError || strings.ContainsAny(toolError(res), "\x1b\x07") {
			t.Fatalf("%s: %q", tool, toolError(res))
		}
	}
	if b, _ := os.ReadFile(path); string(b) != body {
		t.Fatalf("rewritten: %s", b)
	}
}
