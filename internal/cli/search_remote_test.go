package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
)

// fakeFindAsana adds the search API, typeahead and the tasks they find (Issue #36) to
// fakePushAsana. POST /batch is not served, so found tasks are read one by one.
type fakeFindAsana struct {
	*fakePushAsana
	searchStatus int // != 0: the search API answers this status
	searches     int
	typeaheads   []string // "user:hanako"
}

func (f *fakeFindAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	switch r.Method + " " + r.URL.Path {
	case "GET /workspaces/w1/tasks/search":
		f.searches++
		if f.searchStatus != 0 {
			w.WriteHeader(f.searchStatus)
			fmt.Fprint(w, `{"errors":[{"message":"Search is only available to premium users."}]}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"gid":"t5"},{"gid":"t1"}]}`)
	case "GET /workspaces/w1/typeahead":
		f.typeaheads = append(f.typeaheads, q.Get("resource_type")+":"+q.Get("query"))
		switch q.Get("resource_type") {
		case "task":
			fmt.Fprint(w, `{"data":[{"gid":"t5","name":"古い請求書"}]}`)
		case "user":
			fmt.Fprint(w, `{"data":[{"gid":"u7","name":"Hanako Yamada","email":"hanako@example.com"}]}`)
		case "tag":
			fmt.Fprint(w, `{"data":[{"gid":"g7","name":"経理"}]}`)
		}
	case "GET /tasks/t5":
		fmt.Fprint(w, `{"data":{"gid":"t5","name":"古い請求書","completed":true,"notes":"\u001b[2J本文",
			"workspace":{"gid":"w1"},"memberships":[{"project":{"gid":"p9","name":"経理"}}]}}`)
	case "POST /tasks":
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"gid": "t50", "name": "請求書を送る",
			"workspace": map[string]string{"gid": "w1"}}})
	case "POST /tasks/t50/addTag":
		fmt.Fprint(w, `{"data":{}}`)
	default:
		f.fakePushAsana.ServeHTTP(w, r)
	}
}

func findCLIEnv(t *testing.T, f *fakeFindAsana) (string, func(args ...string) (string, string, error)) {
	t.Helper()
	path := writeCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	f.fakePushAsana = &fakePushAsana{assignee: "me", section: "s1", sname: "家具"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return path, func(args ...string) (string, string, error) {
		cmd := newRoot(&app{baseURL: srv.URL})
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}
}

func TestSearchRemoteFindsAndCaches(t *testing.T) {
	f := &fakeFindAsana{}
	path, run := findCLIEnv(t, f)
	// without --remote, Asana is not asked
	out, _, err := run("search", "請求書")
	if err != nil || strings.Contains(out, "t5") || f.searches != 0 {
		t.Fatalf("local: %q %v searches %d", out, err, f.searches)
	}
	out, errOut, err := run("search", "請求書", "--remote")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "古い請求書") || !strings.Contains(errOut, "Asana で 2 件見つかりました") {
		t.Fatalf("out %q err %q", out, errOut)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	d, err := st.GetTask(context.Background(), "t5")
	if err != nil || strings.Contains(d.Notes, "\x1b") {
		t.Fatalf("t5 cached: %+v %v", d, err)
	}
	// --json keeps its shape: an array of tasks
	out, _, err = run("--json", "search", "請求書", "--remote", "--completed")
	var tasks []map[string]any
	if err != nil || json.Unmarshal([]byte(out), &tasks) != nil || len(tasks) != 1 || tasks[0]["gid"] != "t5" {
		t.Fatalf("json %q %v", out, err)
	}
}

func TestSearchRemoteWithoutTheSearchAPI(t *testing.T) {
	f := &fakeFindAsana{searchStatus: http.StatusPaymentRequired}
	_, run := findCLIEnv(t, f)
	for i := 0; i < 2; i++ {
		out, errOut, err := run("search", "古い", "--remote")
		if err != nil || !strings.Contains(out, "古い請求書") || !strings.Contains(errOut, "タスク名の先頭で探しました") {
			t.Fatalf("%d: out %q err %q %v", i, out, errOut, err)
		}
	}
	if f.searches != 1 || len(f.typeaheads) != 2 {
		t.Fatalf("search API asked %d times, typeahead %v", f.searches, f.typeaheads)
	}
}

func TestSearchRemoteOffline(t *testing.T) {
	writeCLIEnv(t) // no token
	out, errOut, err := runCLISplit(t, "search", "ソファ", "--remote")
	if err != nil || !strings.Contains(out, "ソファを買う") || !strings.Contains(errOut, "Asana では探せませんでした") {
		t.Fatalf("out %q err %q %v", out, errOut, err)
	}
}

func TestAssignLooksUpAUserTheCacheLacks(t *testing.T) {
	f := &fakeFindAsana{}
	_, run := findCLIEnv(t, f)
	out, errOut, err := run("task", "assign", "t1", "hanako")
	if err != nil {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
	if strings.Join(f.typeaheads, ",") != "user:hanako" || f.assignee != "u7" {
		t.Fatalf("typeahead %v assignee %q", f.typeaheads, f.assignee)
	}
	// a user the cache knows: Asana is not asked
	if _, _, err := run("task", "assign", "t1", "Partner"); err != nil || len(f.typeaheads) != 1 {
		t.Fatalf("%v typeahead %v", err, f.typeaheads)
	}
}

func TestAssignOfflineUnknownUser(t *testing.T) {
	writeCLIEnv(t) // no token: nothing to look up with
	if _, _, err := runCLISplit(t, "task", "assign", "t1", "hanako"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err %v", err)
	}
}

func TestQuickAddLooksUpATagTheCacheLacks(t *testing.T) {
	f := &fakeFindAsana{}
	path, run := findCLIEnv(t, f)
	out, errOut, err := run("task", "create", "--quick", "請求書を送る #経理")
	if err != nil {
		t.Fatalf("%q %q %v", out, errOut, err)
	}
	if strings.Join(f.typeaheads, ",") != "tag:経理" || !strings.Contains(out, "読み取り:") {
		t.Fatalf("typeahead %v out %q", f.typeaheads, out)
	}
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	tags, _ := st.TaskTags(context.Background(), "t50")
	if len(tags) != 1 || tags[0].GID != "g7" {
		t.Fatalf("t50 tags %+v (calls %v)", tags, f.took())
	}
}
