package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// #54: `task list --project` fetches a project whose tasks are not cached; offline (or with
// --offline) it says they are missing and the next sync fetches them.

func lazyCLIServer(t *testing.T) (*httptest.Server, func() []string) {
	var mu gosync.Mutex
	var log []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		log = append(log, r.Method+" "+r.URL.Path)
		mu.Unlock()
		switch p := r.URL.Path; {
		case p == "/users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case p == "/events":
			w.WriteHeader(http.StatusPreconditionFailed)
			fmt.Fprint(w, `{"errors":[{"message":"Sync token invalid"}],"sync":"T1"}`)
		case p == "/projects/p2/sections":
			fmt.Fprint(w, `{"data":[{"gid":"s9","name":"Todo"}]}`)
		case p == "/projects/p2/custom_field_settings", p == "/status_updates":
			fmt.Fprint(w, `{"data":[]}`)
		case p == "/tasks" && q.Get("project") == "p2":
			fmt.Fprint(w, `{"data":[{"gid":"x1","name":"見積もりを出す","assignee":{"gid":"me"},"memberships":[{"project":{"gid":"p2"},"section":{"gid":"s9"}}]}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"errors":[{"message":"not here"}]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), log...)
	}
}

func runLazyCLI(t *testing.T, baseURL string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newTestRoot(baseURL)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func lazyState(t *testing.T, path, gid string) string {
	t.Helper()
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := st.ProjectTaskState(context.Background(), gid)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTaskListFetchesUnfetchedProject(t *testing.T) {
	path := projectCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	srv, requests := lazyCLIServer(t)
	stdout, stderr, err := runLazyCLI(t, srv.URL, "task", "list", "--project", "仕事")
	if err != nil || !strings.Contains(stdout, "見積もりを出す") || !strings.Contains(stderr, "取得しました: タスク 1 件 / リクエスト ") {
		t.Fatalf("task list: %v\nstdout:\n%s\nstderr:\n%s\nrequests %v", err, stdout, stderr, requests())
	}
	if s := lazyState(t, path, "p2"); s != readmodel.TasksAll {
		t.Fatalf("p2 state %q", s)
	}
	n := len(requests())
	if _, _, err := runLazyCLI(t, srv.URL, "task", "list", "--project", "仕事"); err != nil || len(requests()) != n {
		t.Fatalf("cached project asked Asana again: %v %v", err, requests()[n:])
	}
}

func TestTaskListUnfetchedProjectOffline(t *testing.T) {
	path := projectCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	err = st.SetSyncState(context.Background(), store.KeyProjectTasks+"p1", readmodel.TasksAll) // p1 synced
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(auth.EnvToken, "test-pat")
	srv, requests := lazyCLIServer(t)
	var stderr string
	_, stderr, err = runLazyCLI(t, srv.URL, "task", "list", "--project", "仕事", "--offline")
	if err != nil || !strings.Contains(stderr, "まだ取得していません") || !strings.Contains(stderr, "次の同期で取得します") {
		t.Fatalf("--offline: %v\n%s", err, stderr)
	}
	if got := requests(); len(got) != 0 {
		t.Fatalf("--offline asked Asana: %v", got)
	}
	if s := lazyState(t, path, "p2"); s != readmodel.TasksWanted {
		t.Fatalf("p2 state %q, want wanted (the next sync fetches it)", s)
	}
	// Asana unreachable: the same note, with why.
	_, stderr, err = runLazyCLI(t, unreachableAsana(t), "task", "list", "--project", "趣味")
	if err != nil || !strings.Contains(stderr, "まだ取得していません") {
		t.Fatalf("unreachable: %v\n%s", err, stderr)
	}
	out, _, err := runLazyCLI(t, unreachableAsana(t), "project", "list")
	if err != nil || !strings.Contains(out, "仕事  (p2)  [タスク未取得]") || strings.Contains(out, "新居  (p1)  [") {
		t.Fatalf("project list: %v\n%s", err, out)
	}
	_, stderr, err = runLazyCLI(t, unreachableAsana(t), "search", "何か")
	if err != nil || !strings.Contains(stderr, "まだ取得していないプロジェクトが 2 件") {
		t.Fatalf("search: %v\n%s", err, stderr)
	}
}
