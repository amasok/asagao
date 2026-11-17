package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// setField / buildAPIRequest boundaries: odd keys are refused or kept literally, never
// panic, and the body is always valid JSON.
func TestAPIAttackFieldKeys(t *testing.T) {
	for _, key := range []string{
		"", "=v", "[]", "[a]", "a[", "a]", "a[b", "a[b]c", "a[]b", "a[][]", "a[][b]", "a[b][]c", "a[[b]]", "a[b]]", "a[]]", "a[b][",
	} {
		_, err := buildAPIRequest("/x", apiOpts{method: "POST", rawFields: []string{key + "=v"}}, nil)
		if err == nil && key != "a]" {
			t.Errorf("key %q accepted", key)
		}
	}
	// no "=" at all
	if _, err := buildAPIRequest("/x", apiOpts{method: "POST", rawFields: []string{"novalue"}}, nil); err == nil {
		t.Error("field without = accepted")
	}
	// the value may hold "=" and be empty
	req, err := buildAPIRequest("/x", apiOpts{method: "POST", rawFields: []string{"a=b=c", "e="}}, nil)
	if err != nil || string(req.body) != `{"data":{"a":"b=c","e":""}}` {
		t.Errorf("got %s %v", req.body, err)
	}
}

// Conflicting shapes are errors, not silent overwrites (data would be lost).
func TestAPIAttackFieldConflicts(t *testing.T) {
	for _, fs := range [][]string{
		{"a=1", "a=2"}, {"a=1", "a[]=2"}, {"a[]=1", "a=2"}, {"a=1", "a[b]=2"}, {"a[b]=1", "a=2"},
		{"a[b]=1", "a[b]=2"}, {"a[b]=1", "a[b][]=2"}, {"a[b][]=1", "a[b]=2"}, {"a[b][]=1", "a[b][c]=2"},
	} {
		if req, err := buildAPIRequest("/x", apiOpts{method: "POST", rawFields: fs}, nil); err == nil {
			t.Errorf("%v accepted: %s", fs, req.body)
		}
	}
}

// -F typing: only JSON numbers (as written), true/false/null; everything else a string,
// and no -F value makes the body unmarshalable.
func TestAPIAttackTypedValues(t *testing.T) {
	for _, v := range []string{
		"1 ", " 1", "1\n", "\t1", "-", "+1", "01", "1.", ".5", "0x10", "1e", "1e+", "--1", "Infinity", "NaN", "-Infinity",
		"True", "FALSE", "Null", "nil", "", "[1]", "{}", `"x"`, "1,2", "1 2", "٣", "1e999", "-0", "0e0", "9" + strings.Repeat("9", 400),
	} {
		req, err := buildAPIRequest("/x", apiOpts{method: "POST", fields: []string{"k=" + v}}, nil)
		if err != nil {
			t.Errorf("-F k=%q: error %v", v, err)
			continue
		}
		if !json.Valid(req.body) {
			t.Errorf("-F k=%q: body %s is not JSON", v, req.body)
		}
	}
	// strings that look like other types stay strings
	for _, v := range []string{"[1]", "{}", `"x"`, "1,2", "NaN", "True", " 1", "0x10"} {
		req, _ := buildAPIRequest("/x", apiOpts{method: "POST", fields: []string{"k=" + v}}, nil)
		var got struct{ Data struct{ K any } }
		if err := json.Unmarshal(req.body, &got); err != nil {
			t.Fatal(err)
		}
		if s, ok := got.Data.K.(string); !ok || s != v {
			t.Errorf("-F k=%q became %#v", v, got.Data.K)
		}
	}
}

// GET fields are encoded into the query: nothing can inject another parameter, a new
// path, a fragment or a header through a value or a key.
func TestAPIAttackQueryInjection(t *testing.T) {
	req, err := buildAPIRequest("/tasks", apiOpts{method: "GET", rawFields: []string{
		"a=1&admin=true", "b=#frag", "c=x\r\nHost: evil", "d=../../x", "e=%2e%2e", "f=\x1b[2J", "g&h=1",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(req.path, "\r\n\x1b #") {
		t.Errorf("unsafe characters in %q", req.path)
	}
	// "g&h" is one key
	if strings.Count(req.path, "&") != 6 {
		t.Errorf("parameters were split: %q", req.path)
	}
}

// --input boundaries.
func TestAPIAttackInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		in     string
	}{
		{"empty", "POST", ""}, {"invalid", "POST", "{"}, {"trailing", "POST", "{} x"}, {"two docs", "POST", "{}{}"},
		{"GET with input", "GET", "{}"}, {"DELETE with input", "DELETE", "{}"},
	} {
		if _, err := buildAPIRequest("/x", apiOpts{method: tc.method, input: "-"}, strings.NewReader(tc.in)); err == nil {
			t.Errorf("%s: accepted", tc.name)
		}
	}
	if _, err := buildAPIRequest("/x", apiOpts{method: "POST", input: "/nonexistent/file.json"}, nil); err == nil {
		t.Error("missing file accepted")
	}
	// bodies that are valid JSON are sent unchanged, even a bare scalar or non-UTF-8 strings
	for _, in := range []string{"null", "1", `"s"`, "[]", "{\"a\":\"\xff\"}"} {
		req, err := buildAPIRequest("/x", apiOpts{method: "PUT", input: "-"}, strings.NewReader(in))
		if err != nil || string(req.body) != in {
			t.Errorf("%q: got %q %v", in, req.body, err)
		}
	}
}

// Methods: unknown, empty or odd-cased are refused or normalised; PATCH/HEAD/CONNECT are not sent.
func TestAPIAttackMethods(t *testing.T) {
	for _, m := range []string{"", "PATCH", "HEAD", "OPTIONS", "CONNECT", "TRACE", "GET ", "GE T", "G\x00T", "POST\r\nHost: x"} {
		if _, err := buildAPIRequest("/x", apiOpts{method: m}, nil); err == nil {
			t.Errorf("method %q accepted", m)
		}
	}
	if req, err := buildAPIRequest("/x", apiOpts{method: "pOsT"}, nil); err != nil || req.method != "POST" {
		t.Errorf("pOsT: %v %v", req.method, err)
	}
}

// --paginate only with GET, also with the path's own query and a lowercase method.
func TestAPIAttackPaginateMethod(t *testing.T) {
	for _, m := range []string{"POST", "PUT", "DELETE", "post"} {
		if _, err := buildAPIRequest("/x", apiOpts{method: m, paginate: true}, nil); err == nil {
			t.Errorf("--paginate with %s accepted", m)
		}
	}
}

// --dry-run sends nothing for any method, shows the request, and never needs a login.
func TestAPIAttackDryRunSendsNothing(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { t.Errorf("dry-run sent %s %s", r.Method, r.URL) }}
	for _, args := range [][]string{
		{"/tasks", "--dry-run"},
		{"-X", "POST", "/tasks", "-f", "name=x", "--dry-run"},
		{"-X", "PUT", "/tasks/1", "-F", "completed=true", "--dry-run"},
		{"-X", "DELETE", "/tasks/1", "--dry-run"},
		{"-X", "POST", "/tasks", "--input", "-", "--dry-run"},
		{"/tasks", "--paginate", "--dry-run"},
	} {
		out, _, err := runAPICLI(t, f, `{"data":{}}`, args...)
		if err != nil {
			t.Errorf("%v: %v", args, err)
		}
		if !strings.Contains(out, "/tasks") {
			t.Errorf("%v: nothing shown: %q", args, out)
		}
		if strings.Contains(out, "test-pat") {
			t.Errorf("%v: token in dry-run output", args)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("calls: %v", f.calls)
	}
}

// Dry-run does not print terminal control characters from the arguments.
func TestAPIAttackDryRunSafeOutput(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {}}
	out, _, err := runAPICLI(t, f, "", "-X", "POST", "/tasks", "-f", "name=\x1b]52;c;eA==\x07\u009b2J", "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out, "\x1b\x07\u009b") {
		t.Errorf("control characters in %q", out)
	}
}

// The token is in no output or error, even when the server and a 401/500 body talk about it.
func TestAPIAttackTokenNotPrinted(t *testing.T) {
	for _, status := range []int{200, 400, 401, 403, 404} {
		f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"errors":[{"message":"bad"}]}`)
		}}
		out, errOut, err := runAPICLI(t, f, "", "/users/me")
		all := out + errOut
		if err != nil {
			all += err.Error()
		}
		if strings.Contains(all, "test-pat") {
			t.Errorf("status %d: token printed: %q", status, all)
		}
		if len(f.calls) == 0 || !strings.Contains(f.calls[0], "Bearer test-pat") {
			t.Errorf("status %d: the request carried no token: %v", status, f.calls)
		}
	}
}

// Error bodies are printed through termsafe, and a >= 400 is an error (exit 1).
func TestAPIAttackErrorBodySafe(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, "<html>\x1b]0;pwn\x07\u009b2J oops</html>")
	}}
	out, errOut, err := runAPICLI(t, f, "", "/x")
	if err == nil {
		t.Error("400 gave no error")
	}
	if strings.ContainsAny(out+errOut, "\x1b\x07\u009b") {
		t.Errorf("control characters: %q %q", out, errOut)
	}
	if err != nil && strings.ContainsAny(err.Error(), "\x1b\x07\u009b") {
		t.Errorf("control characters in error: %q", err)
	}
}

// Empty bodies (204), a non-UTF-8 body and a huge one.
func TestAPIAttackOddBodies(t *testing.T) {
	for name, body := range map[string]string{"empty": "", "invalid utf8": "\xff\xfe text", "huge": strings.Repeat("x", 5<<20)} {
		f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }}
		out, _, err := runAPICLI(t, f, "", "/x")
		if err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if !utf8.ValidString(out) {
			t.Errorf("%s: raw invalid bytes printed: %q", name, out)
		}
	}
}

// --paginate: where it stops and what it does with odd pages.
func TestAPIAttackPaginate(t *testing.T) {
	pages := func(replies ...string) (*fakeRawAsana, *int) {
		n := 0
		return &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
			if n >= len(replies) {
				t.Errorf("page %d requested but only %d exist", n, len(replies))
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			fmt.Fprint(w, replies[n])
			n++
		}}, &n
	}
	t.Run("empty offset ends", func(t *testing.T) {
		f, n := pages(`{"data":[1],"next_page":{"offset":""}}`)
		out, _, err := runAPICLI(t, f, "", "/x", "--paginate")
		if err != nil || *n != 1 || !strings.Contains(out, "1") {
			t.Errorf("%v %d %q", err, *n, out)
		}
	})
	t.Run("null data on a page", func(t *testing.T) {
		f, _ := pages(`{"data":null,"next_page":{"offset":"a"}}`, `{"data":[2]}`)
		out, _, err := runAPICLI(t, f, "", "/x", "--paginate")
		if err != nil || !strings.Contains(out, "2") {
			t.Errorf("%v %q", err, out)
		}
	})
	t.Run("object data is refused", func(t *testing.T) {
		f, _ := pages(`{"data":{"gid":"1"}}`)
		if _, _, err := runAPICLI(t, f, "", "/x", "--paginate"); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("not JSON is an error, not a panic", func(t *testing.T) {
		f, _ := pages(`oops`)
		if _, _, err := runAPICLI(t, f, "", "/x", "--paginate"); err == nil {
			t.Error("accepted")
		}
	})
	t.Run("an error on page 2 is an error and no half result is printed as success", func(t *testing.T) {
		n := 0
		f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
			n++
			if n == 1 {
				fmt.Fprint(w, `{"data":[1],"next_page":{"offset":"a"}}`)
				return
			}
			w.WriteHeader(400)
			fmt.Fprint(w, `{"errors":[{"message":"boom"}]}`)
		}}
		_, _, err := runAPICLI(t, f, "", "/x", "--paginate")
		if err == nil {
			t.Error("no error")
		}
	})
	t.Run("a cycle of two offsets ends", func(t *testing.T) {
		n := 0
		f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
			n++
			if n > 50 {
				t.Error("never stopped")
				fmt.Fprint(w, `{"data":[]}`)
				return
			}
			off := []string{"a", "b"}[n%2]
			fmt.Fprintf(w, `{"data":[%d],"next_page":{"offset":%q}}`, n, off)
		}}
		if _, _, err := runAPICLI(t, f, "", "/x", "--paginate"); err == nil {
			t.Error("no error for a cycle")
		}
	})
	t.Run("a user limit is kept, offset is sent as a parameter", func(t *testing.T) {
		n := 0
		f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("limit") != "5" {
				t.Errorf("limit = %q", r.URL.Query().Get("limit"))
			}
			n++
			if n == 1 {
				fmt.Fprint(w, `{"data":[1],"next_page":{"offset":"a&b=1 #x"}}`)
				return
			}
			if r.URL.Query().Get("offset") != "a&b=1 #x" || r.URL.Query().Get("b") != "" {
				t.Errorf("offset not passed intact: %q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"data":[2]}`)
		}}
		if _, _, err := runAPICLI(t, f, "", "/x", "-f", "limit=5", "--paginate"); err != nil {
			t.Error(err)
		}
	})
	t.Run("429 is retried and the result is whole", func(t *testing.T) {
		n := 0
		f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
			n++
			if n == 2 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(429)
				return
			}
			if n == 1 {
				fmt.Fprint(w, `{"data":[1],"next_page":{"offset":"a"}}`)
				return
			}
			fmt.Fprint(w, `{"data":[2]}`)
		}}
		out, _, err := runAPICLI(t, f, "", "/x", "--paginate")
		if err != nil || !strings.Contains(out, "1") || !strings.Contains(out, "2") {
			t.Errorf("%v %q", err, out)
		}
	})
	t.Run("output control characters", func(t *testing.T) {
		f, _ := pages("{\"data\":[{\"n\":\"\x7f\u009b\"}]}")
		out, _, err := runAPICLI(t, f, "", "/x", "--paginate")
		if err != nil || strings.ContainsAny(out, "\x7f\u009b") {
			t.Errorf("%v %q", err, out)
		}
	})
}

// A 3xx answer is an error (exit 1) for every method, and nothing is followed.
func TestAPIAttackRedirect(t *testing.T) {
	for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
		f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://evil.example/x", 307)
		}}
		if _, _, err := runAPICLI(t, f, "", "-X", m, "/x"); err == nil {
			t.Errorf("%s: 307 gave no error", m)
		}
		if len(f.calls) != 1 {
			t.Errorf("%s: %d calls", m, len(f.calls))
		}
	}
}

// Paths that are refused never send, for every method, even with fields and --input.
func TestAPIAttackRefusedPathsSendNothing(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { t.Errorf("sent %s", r.URL) }}
	for _, p := range []string{
		"http://127.0.0.1/x", "HTTPS://evil.example", "//evil.example", "/\\evil.example", "\\\\evil.example", "/a/%2E%2E/b",
		"/a%2f%2e%2e%2fb", "ftp://x", "x://y", "/x#y",
	} {
		for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
			if _, _, err := runAPICLI(t, f, `{}`, "-X", m, p, "-f", "a=1"); err == nil {
				t.Errorf("%s %q: no error", m, p)
			}
		}
	}
}

// --dry-run --json is one valid JSON document, sends nothing, and keeps bodies as given
// (a bare scalar, a body with terminal escapes, no body at all).
func TestAPIAttackDryRunJSON(t *testing.T) {
	f := &fakeRawAsana{reply: func(w http.ResponseWriter, r *http.Request) { t.Errorf("sent %s", r.URL) }}
	for _, in := range []string{"", "null", "1", `"s"`, "[]", "{\"a\":\"\x7f\u009b\\u001b\"}", "  {\"a\":1}\n\n"} {
		args := []string{"--json", "-X", "POST", "/tasks", "--dry-run"}
		if in != "" {
			args = append(args, "--input", "-", "-f", "k=v")
		}
		out, _, err := runAPICLI(t, f, in, args...)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		var got struct {
			DryRun bool            `json:"dry_run"`
			Method string          `json:"method"`
			Path   string          `json:"path"`
			Body   json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil || !got.DryRun || got.Method != "POST" || !strings.HasPrefix(got.Path, "/tasks") {
			t.Errorf("%q: %v %s", in, err, out)
		}
		if strings.ContainsAny(out, "\x7f\u009b\x1b") {
			t.Errorf("%q: control characters printed: %q", in, out)
		}
		if (in == "") != (len(got.Body) == 0) {
			t.Errorf("%q: body %s", in, got.Body)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("calls %v", f.calls)
	}
}
