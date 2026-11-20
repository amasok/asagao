package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/termsafe"
)

func TestBuildAPIRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		path     string
		o        apiOpts
		wantPath string
		wantBody string
	}{
		{"get", "users/me", apiOpts{method: "get"}, "/users/me", ""},
		{"get fields go to the query", "/tasks/1?opt_fields=name", apiOpts{method: "GET", rawFields: []string{"opt_pretty=true", "x=a b&c"}},
			"/tasks/1?opt_fields=name&opt_pretty=true&x=a+b%26c", ""},
		{"delete fields go to the query", "/tasks/1", apiOpts{method: "DELETE", fields: []string{"a=1"}}, "/tasks/1?a=1", ""},
		{"post body", "/tasks", apiOpts{method: "POST",
			rawFields: []string{"name=買う", "projects[]=1", "projects[]=2", "custom_fields[9]=高", "completed=true"},
			fields:    []string{"done=true", "n=12", "f=-1.5e3", "z=null", "s=0x1", "b=false", "big=12345678901234567890"}},
			"/tasks", `{"data":{"b":false,"big":12345678901234567890,"completed":"true","custom_fields":{"9":"高"},"done":true,"f":-1.5e3,"n":12,"name":"買う","projects":["1","2"],"s":"0x1","z":null}}`},
		{"-F number with spaces is a string", "/x", apiOpts{method: "PUT", fields: []string{"a=1 ", "b=2\n", "c= 3"}},
			"/x", `{"data":{"a":"1 ","b":"2\n","c":" 3"}}`},
		{"put nested append", "/x", apiOpts{method: "PUT", rawFields: []string{"a[b][]=1", "a[b][]=2", "a[c]=3"}},
			"/x", `{"data":{"a":{"b":["1","2"],"c":"3"}}}`},
		{"URL in the query", "/x?u=https://example.com/a", apiOpts{method: "GET"}, "/x?u=https://example.com/a", ""},
		{"post without fields has no body", "/tasks/1/duplicate", apiOpts{method: "POST"}, "/tasks/1/duplicate", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := buildAPIRequest(tc.path, tc.o, nil)
			if err != nil {
				t.Fatal(err)
			}
			if req.path != tc.wantPath || string(req.body) != tc.wantBody {
				t.Errorf("got %s %s %s\nwant path %s body %s", req.method, req.path, req.body, tc.wantPath, tc.wantBody)
			}
		})
	}
}

// --input is sent unchanged, and the fields then go to the query.
func TestBuildAPIRequestInput(t *testing.T) {
	in := `{"data": {"name": "x"}}` + "\n"
	req, err := buildAPIRequest("/tasks", apiOpts{method: "POST", input: "-", rawFields: []string{"opt_fields=name"}}, strings.NewReader(in))
	if err != nil || req.path != "/tasks?opt_fields=name" || string(req.body) != in {
		t.Fatalf("got %q %q %v", req.path, req.body, err)
	}
	f := filepath.Join(t.TempDir(), "b.json")
	if err := os.WriteFile(f, []byte(`[1]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if req, err := buildAPIRequest("/x", apiOpts{method: "PUT", input: f}, nil); err != nil || string(req.body) != `[1]` {
		t.Errorf("file: %q %v", req.body, err)
	}
}

func TestBuildAPIRequestErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  string
		o     apiOpts
		stdin string
	}{
		{"absolute URL", "https://evil.example/x", apiOpts{method: "GET"}, ""},
		{"asana URL too", "https://app.asana.com/api/1.0/users/me", apiOpts{method: "GET"}, ""},
		{"scheme relative", "//evil.example/x", apiOpts{method: "GET"}, ""},
		{"host without scheme", "evil.example:443/x", apiOpts{method: "GET"}, ""},
		{"dot dot", "/tasks/../../x", apiOpts{method: "GET"}, ""},
		{"encoded dot dot", "/%2e%2e/x", apiOpts{method: "GET"}, ""},
		{"unknown method", "/x", apiOpts{method: "PATCH"}, ""},
		{"method injection", "/x", apiOpts{method: "GET /evil"}, ""},
		{"field without =", "/x", apiOpts{method: "POST", rawFields: []string{"name"}}, ""},
		{"empty key", "/x", apiOpts{method: "POST", rawFields: []string{"=v"}}, ""},
		{"duplicate key", "/x", apiOpts{method: "POST", rawFields: []string{"a=1", "a=2"}}, ""},
		{"array and scalar", "/x", apiOpts{method: "POST", rawFields: []string{"a=1", "a[]=2"}}, ""},
		{"object and scalar", "/x", apiOpts{method: "POST", rawFields: []string{"a[b]=1", "a=2"}}, ""},
		{"[] in the middle", "/x", apiOpts{method: "POST", rawFields: []string{"a[][b]=1"}}, ""},
		{"bad brackets", "/x", apiOpts{method: "POST", rawFields: []string{"a[b=1"}}, ""},
		{"text after brackets", "/x", apiOpts{method: "POST", rawFields: []string{"a[b]c=1"}}, ""},
		{"input not JSON", "/x", apiOpts{method: "POST", input: "-"}, "name=x"},
		{"input on GET", "/x", apiOpts{method: "GET", input: "-"}, "{}"},
		{"input file missing", "/x", apiOpts{method: "POST", input: filepath.Join(t.TempDir(), "none.json")}, ""},
		{"paginate POST", "/x", apiOpts{method: "POST", paginate: true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if req, err := buildAPIRequest(tc.path, tc.o, strings.NewReader(tc.stdin)); err == nil {
				t.Errorf("no error: %+v", req)
			}
		})
	}
}

// fakeRawAsana answers `asagao api` requests from a table and records them.
type fakeRawAsana struct {
	mu    gosync.Mutex
	calls []string
	reply func(w http.ResponseWriter, r *http.Request)
}

func (f *fakeRawAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.calls = append(f.calls, strings.TrimSpace(r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("Authorization")+" "+string(b)))
	f.mu.Unlock()
	f.reply(w, r)
}

// runAPICLI runs `asagao api ...` against f with a PAT login and private folders.
func runAPICLI(t *testing.T, f *fakeRawAsana, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv(auth.EnvToken, "test-pat")
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cmd := newTestRoot(srv.URL)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(stdin))
	cmd.SetArgs(append([]string{"api"}, args...))
	err = cmd.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

// The answer is printed as indented JSON, with control characters escaped (still valid
// JSON, same values), and the request carries the login's token.
func TestAPICommandPrintsSafeJSON(t *testing.T) {
	body := "{\"data\":{\"gid\":\"me\",\"name\":\"\\u001b]52;c;eA==\\u0007Me\u009b2J\"}}"
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }}
	out, _, err := runAPICLI(t, f, "", "/users/me", "-f", "opt_fields=name")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || f.calls[0] != "GET /users/me?opt_fields=name Bearer test-pat" {
		t.Errorf("calls = %q", f.calls)
	}
	if !termsafe.Safe(out) {
		t.Errorf("output has control characters: %q", out)
	}
	var got, want any
	_ = json.Unmarshal([]byte(body), &want)
	if err := json.Unmarshal([]byte(out), &got); err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("output = %s (%v)", out, err)
	}
	if !strings.HasPrefix(out, "{\n  \"data\": {") {
		t.Errorf("not indented: %s", out)
	}
}

// A write sends its body; an error answer is printed and makes the command fail, without
// the token in the output or the error.
func TestAPICommandWriteAndError(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"errors":[{"message":"no \u001b[31maccess"}]}`)
	}}
	out, errOut, err := runAPICLI(t, f, "", "-X", "put", "/tasks/1", "-F", "completed=true")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	if want := `PUT /tasks/1 Bearer test-pat {"data":{"completed":true}}`; len(f.calls) != 1 || f.calls[0] != want {
		t.Errorf("calls = %q", f.calls)
	}
	if !strings.Contains(out, `"message": "no \u001b[31maccess"`) {
		t.Errorf("error body not printed: %s", out)
	}
	if !termsafe.Safe(err.Error()) {
		t.Errorf("error has control characters: %q", err.Error())
	}
	for _, s := range []string{out, errOut, err.Error()} {
		if strings.Contains(s, "test-pat") {
			t.Errorf("token in output: %q", s)
		}
	}
}

// A POST that got a 5xx is not sent again (it may have been applied).
func TestAPICommandDoesNotRepeatPOST(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }}
	_, _, err := runAPICLI(t, f, "", "-X", "POST", "/tasks", "-f", "name=x")
	if err == nil || len(f.calls) != 1 {
		t.Errorf("err = %v, %d calls", err, len(f.calls))
	}
}

// --dry-run prints the request and sends nothing.
func TestAPICommandDryRun(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { t.Error("request sent") }}
	out, _, err := runAPICLI(t, f, `{"data":{"name":"x\u0085y"}}`, "-X", "POST", "/tasks", "--input", "-", "-f", "opt_fields=name", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	want := "POST /tasks?opt_fields=name\n{\n  \"data\": {\n    \"name\": \"x\\u0085y\"\n  }\n}\n"
	if out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
}

// A path outside the API is refused before the token is looked up or anything is sent.
func TestAPICommandRefusesOtherHosts(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { t.Error("request sent") }}
	for _, p := range []string{"https://evil.example/x", "//evil.example/x", "/../x", "/%2e%2e/x"} {
		if _, _, err := runAPICLI(t, f, "", p); err == nil {
			t.Errorf("%q: no error", p)
		}
	}
}

// --paginate follows next_page and prints the data of every page in one array.
func TestAPICommandPaginate(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "100" || q.Get("opt_fields") != "name" {
			t.Errorf("query = %v", q)
		}
		switch q.Get("offset") {
		case "":
			fmt.Fprint(w, `{"data":[{"gid":"1"}],"next_page":{"offset":"o2","path":"/x","uri":"https://evil.example/"}}`)
		case "o2":
			fmt.Fprint(w, `{"data":[{"gid":"2"},{"gid":"3"}],"next_page":null}`)
		}
	}}
	out, _, err := runAPICLI(t, f, "", "/projects/p/tasks", "-f", "opt_fields=name", "--paginate")
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Data []struct{ GID string } }
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got.Data) != 3 || got.Data[2].GID != "3" {
		t.Errorf("out = %s (%v)", out, err)
	}
	if len(f.calls) != 2 || !strings.HasPrefix(f.calls[1], "GET /projects/p/tasks?") {
		t.Errorf("calls = %q", f.calls)
	}
}

// A page that sends back the same offset again ends the loop with an error.
func TestAPICommandPaginateStopsOnRepeatedOffset(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":[],"next_page":{"offset":"same"}}`)
	}}
	if _, _, err := runAPICLI(t, f, "", "/x", "--paginate"); err == nil || len(f.calls) > 3 {
		t.Errorf("err = %v, %d calls", err, len(f.calls))
	}
}

// A redirect is not followed and fails the command.
func TestAPICommandRedirect(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}}
	if _, _, err := runAPICLI(t, f, "", "/users/me"); err == nil || len(f.calls) != 1 {
		t.Errorf("err = %v, calls = %q", err, f.calls)
	}
}

// What is not JSON is printed through termsafe.
func TestAPICommandNonJSON(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "<html>\x1b]8;;evil\x07bad gateway</html>")
	}}
	out, _, err := runAPICLI(t, f, "", "-X", "POST", "/x")
	if err == nil || !termsafe.Safe(out) || !strings.Contains(out, "bad gateway") {
		t.Errorf("out = %q, err = %v", out, err)
	}
}

// Without a login it says so and sends nothing.
func TestAPICommandNotLoggedIn(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { t.Error("request sent") }}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv := httptest.NewServer(f)
	defer srv.Close()
	t.Setenv(auth.EnvToken, "")
	cmd := newTestRoot(srv.URL)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"api", "/users/me"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Error("no error without a login")
	}
}

// --dry-run --json prints the request as JSON with "dry_run": true, as the write commands do.
func TestAPICommandDryRunJSON(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { t.Error("request sent") }}
	out, _, err := runAPICLI(t, f, "", "-X", "POST", "/tasks", "-f", "name=x", "--dry-run", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		DryRun bool `json:"dry_run"`
		Method string
		Path   string
		Body   struct{ Data struct{ Name string } }
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || !got.DryRun || got.Method != "POST" || got.Path != "/tasks" || got.Body.Data.Name != "x" {
		t.Errorf("out = %s (%v)", out, err)
	}
}
