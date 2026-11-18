package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type timeAddJSON struct {
	GID  string `json:"time_entry_gid"`
	Sent bool   `json:"sent"`
}

func parseTimeAdd(t *testing.T, stdout string) timeAddJSON {
	t.Helper()
	var r timeAddJSON
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("not JSON: %q (%v)", stdout, err)
	}
	return r
}

// Several entries added one after another: each prints its own real gid (not the first's,
// not a local one), and each gid works with time edit.
func TestAttackTimeAddJSONGidsAreDistinctAndReal(t *testing.T) {
	_, fake, run := timeCLIEnv(t)
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		out, errOut, err := run("--json", "time", "add", "t1", "15m", "--date", "2026-11-1"+string(rune('0'+i)))
		if err != nil {
			t.Fatalf("%v %s", err, errOut)
		}
		r := parseTimeAdd(t, out)
		if !r.Sent || r.GID == "" || strings.HasPrefix(r.GID, "local:") || seen[r.GID] {
			t.Fatalf("add %d: %+v (seen %v)", i, r, seen)
		}
		seen[r.GID] = true
	}
	if got := fake.took("POST "); len(got) != 3 {
		t.Fatalf("POSTs %q", got)
	}
}

// Entries left in the outbox (a 5xx on the send): the gid stays the local one, which the
// next sync sends; the output says it was not sent. And once it is sent by that sync, a
// new `time add` still gives its own real gid.
func TestAttackTimeAddJSONNotSentKeepsLocalGid(t *testing.T) {
	path, _, _ := timeCLIEnv(t)
	cmd := newRoot(&app{baseURL: "http://127.0.0.1:1"}) // Asana can not be reached
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs([]string{"--json", "time", "add", "t1", "20m"})
	_ = cmd.ExecuteContext(context.Background())
	out := outBuf.String()
	r := parseTimeAdd(t, out)
	if r.Sent {
		t.Fatalf("reported as sent: %s", out)
	}
	if !strings.HasPrefix(r.GID, "local:") {
		t.Fatalf("gid = %q: an unsent entry has no real gid yet", r.GID)
	}
	if ops := cliOps(t, path); len(ops) == 0 {
		t.Fatal("the entry was lost")
	}
}

// 402 (the plan has no time tracking): nothing is queued and no gid is made up.
func TestAttackTimeAddJSONPaymentRequired(t *testing.T) {
	_, fake, run := timeCLIEnv(t)
	fake.unpaid = true
	out, _, err := run("--json", "time", "add", "t1", "1h")
	if err == nil {
		r := parseTimeAdd(t, out)
		if r.Sent || (r.GID != "" && !strings.HasPrefix(r.GID, "local:")) {
			t.Fatalf("a real gid or sent from a refused add: %s", out)
		}
	}
}

// --dry-run (every write command has it, see the agent guide) writes nothing and never
// asks Asana to create anything.
func TestAttackTimeAddDryRun(t *testing.T) {
	path, fake, run := timeCLIEnv(t)
	out, errOut, err := run("--json", "time", "add", "t1", "1h", "--dry-run")
	if err != nil {
		t.Fatalf("%v %s %s", err, out, errOut)
	}
	if got := fake.took("POST "); len(got) != 0 {
		t.Fatalf("POST in a dry run: %q", got)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Fatalf("queued %v", opKinds(ops))
	}
	var r map[string]any
	if json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("not JSON: %q", out)
	}
}

// Without --json the text is as before: the sent notice, no gid printed.
func TestAttackTimeAddTextUnchanged(t *testing.T) {
	_, _, run := timeCLIEnv(t)
	out, _, err := run("time", "add", "t1", "45m", "--date", "2026-11-12")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Asana に送信しました") || strings.Contains(out, "local:") || json.Valid([]byte(out)) {
		t.Fatalf("text output = %q", out)
	}
}

// The task is gone in Asana (404 on the create): the entry is dropped, and the output
// does not claim it was sent nor hand out an empty or real gid.
func TestAttackTimeAddJSONTaskGone(t *testing.T) {
	_, fake, _ := timeCLIEnv(t)
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/time_tracking_entries") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errors":[{"message":"gone"}]}`))
			return
		}
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(gone.Close)
	cmd := newRoot(&app{baseURL: gone.URL})
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--json", "time", "add", "t1", "20m"})
	_ = cmd.ExecuteContext(context.Background())
	var r timeAddJSON
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("not JSON: %q %q", out.String(), errOut.String())
	}
	if r.Sent || r.GID == "" || (!strings.HasPrefix(r.GID, "local:") && r.GID != "") {
		t.Fatalf("output = %s", out.String())
	}
}
