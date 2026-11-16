package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/amasok/asana-tui/internal/auth"
	"github.com/amasok/asana-tui/internal/store"
)

// --json must be exactly one JSON document on stdout, nothing on stderr from the command's
// own chatter, and it must not carry raw terminal controls even when the text comes from an
// Asana error that never went through the store (the JSON path skips the termsafe writer).
func TestJSONOutputFromAsanaErrorHasNoRawControls(t *testing.T) {
	path := writeCLIEnv(t)
	_ = path
	t.Setenv(auth.EnvToken, "test-pat")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /tasks/t1":
			w.Write([]byte(`{"data":{"gid":"t1","name":"x","completed":false,"workspace":{"gid":"w1"}}}`))
		default:
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"errors":[{"message":"bad ` + `\u001b[2J\u001b]52;c;ZXZpbA==\u0007\u009b2J\u009d0;t\u009c\r\u007f‮` + `"}]}`))
		}
	}))
	defer srv.Close()
	cmd := newRoot(&app{baseURL: srv.URL})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--json", "task", "complete", "t1"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("%v\n%s%s", err, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "bad") {
		t.Fatalf("the Asana error did not reach the output, test is vacuous: %q", out.String())
	}
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("stdout is not JSON: %q", out.String())
	}
	if dec.More() {
		t.Errorf("more than one document on stdout: %q", out.String())
	}
	for _, r := range out.String() {
		if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Errorf("raw control %U in --json output:\n%q", r, out.String())
			break
		}
	}
	if !utf8.ValidString(out.String()) || strings.Contains(errOut.String(), "\x1b") {
		t.Errorf("stdout valid=%v stderr=%q", utf8.ValidString(out.String()), errOut.String())
	}
}

func jsonRunner(t *testing.T, h http.Handler) func(args ...string) (string, string, error) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return func(args ...string) (string, string, error) {
		cmd := newRoot(&app{baseURL: srv.URL})
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}
}

// A task created online has its real gid in Asana; the --json result must not hand out the
// local one, and a task that Asana no longer has must not be reported as sent.
func TestWriteJSONCreateAndDeletedTask(t *testing.T) {
	writeCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &fakePushAsana{assignee: "me", section: "s1", sname: "家具"}
	deleted := false
	run := jsonRunner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.Method + " " + r.URL.Path; {
		case p == "POST /tasks":
			w.Write([]byte(`{"data":{"gid":"real1","name":"新規","workspace":{"gid":"w1"}}}`))
		case deleted && strings.HasSuffix(p, "/tasks/t1"):
			w.WriteHeader(404)
			w.Write([]byte(`{"errors":[{"message":"task: Not a recognized ID"}]}`))
		default:
			fake.ServeHTTP(w, r)
		}
	}))
	stdout, _, err := run("--json", "task", "create", "新規タスク", "--project", "新居")
	if err != nil {
		t.Fatal(err)
	}
	var r writeResult
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("%q: %v", stdout, err)
	}
	if !r.Sent || store.IsLocalGID(r.TaskGID) {
		t.Errorf("create online: %+v (sent, and no local gid)", r)
	}

	deleted = true
	stdout, _, err = run("--json", "task", "complete", "t1")
	if err != nil {
		t.Fatal(err)
	}
	r = writeResult{}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("%q: %v", stdout, err)
	}
	if r.Sent {
		t.Errorf("a change to a task deleted in Asana was reported as sent: %s", stdout)
	}
}
