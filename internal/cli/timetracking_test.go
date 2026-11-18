package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/store"
)

// fakeTimeAsana answers the time tracking endpoints of t1 like Asana; unpaid answers them
// with 402.
type fakeTimeAsana struct {
	mu      gosync.Mutex
	calls   []string
	unpaid  bool
	entries []map[string]any
	made    int
}

func (f *fakeTimeAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	f.calls = append(f.calls, r.Method+" "+r.URL.Path+" "+string(bytes.TrimSpace(raw)))
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	if r.URL.Path == "/users/me" {
		reply(map[string]string{"gid": "me", "name": "Me"})
		return
	}
	if f.unpaid {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = w.Write([]byte(`{"errors":[{"message":"Payment Required"}]}`))
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /tasks/t1/time_tracking_entries":
		reply(f.entries)
	case "POST /tasks/t1/time_tracking_entries":
		var body struct{ Data map[string]any }
		_ = json.Unmarshal(raw, &body)
		gid := "te9"
		if f.made++; f.made > 1 {
			gid = fmt.Sprintf("te9-%d", f.made)
		}
		e := map[string]any{"gid": gid, "duration_minutes": body.Data["duration_minutes"], "entered_on": body.Data["entered_on"],
			"created_by": map[string]string{"gid": "me", "name": "Me"}, "created_at": "2026-11-13T00:00:00Z"}
		f.entries = append(f.entries, e)
		w.WriteHeader(http.StatusCreated)
		reply(e)
	default:
		if gid, ok := strings.CutPrefix(r.URL.Path, "/time_tracking_entries/"); ok && r.Method == http.MethodPut {
			var body struct{ Data map[string]any }
			_ = json.Unmarshal(raw, &body)
			for _, e := range f.entries {
				if e["gid"] == gid {
					for k, v := range body.Data {
						e[k] = v
					}
					reply(e)
					return
				}
			}
		}
		http.Error(w, `{"errors":[{"message":"not in the fake"}]}`, http.StatusNotFound)
	}
}

func (f *fakeTimeAsana) took(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func timeCLIEnv(t *testing.T) (string, *fakeTimeAsana, func(args ...string) (string, string, error)) {
	t.Helper()
	path := writeCLIEnv(t)
	t.Setenv(auth.EnvToken, "test-pat")
	fake := &fakeTimeAsana{entries: []map[string]any{{"gid": "te1", "duration_minutes": 30, "entered_on": "2026-11-10",
		"assignee": map[string]string{"gid": "u2", "name": "Partner"}, "attributable_to": map[string]string{"gid": "p1", "name": "新居"}}}}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	run := func(args ...string) (string, string, error) {
		cmd := newRoot(&app{baseURL: srv.URL})
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		err := cmd.ExecuteContext(context.Background())
		return out.String(), errOut.String(), err
	}
	return path, fake, run
}

// time add sends the minutes and the day at once; time list reads the entries from Asana and
// shows them with their total.
func TestTimeAddAndList(t *testing.T) {
	path, fake, run := timeCLIEnv(t)
	out, errOut, err := run("time", "add", "t1", "1h30m", "--date", "2026-11-12")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	if !strings.Contains(out, "Asana に送信しました") {
		t.Errorf("not sent:\n%s%s", out, errOut)
	}
	if got := fake.took("POST "); len(got) != 1 || !strings.HasSuffix(got[0], `{"data":{"duration_minutes":90,"entered_on":"2026-11-12"}}`) {
		t.Errorf("POST %q", got)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("left in the outbox: %v", opKinds(ops))
	}
	out, errOut, err = run("time", "list", "t1")
	if err != nil {
		t.Fatalf("%v\n%s%s", err, out, errOut)
	}
	for _, want := range []string{"te1", "2026-11-10", "30m", "Partner", "新居", "te9", "1h30m", "合計 2h（2 件）"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// --json gives the sent entry's real gid, which time edit / delete take (#31).
	stdout, _, err := run("--json", "time", "add", "t1", "30m", "--date", "2026-11-13")
	var added struct {
		GID  string `json:"time_entry_gid"`
		Sent bool   `json:"sent"`
	}
	if err != nil || json.Unmarshal([]byte(stdout), &added) != nil || !added.Sent || added.GID == "" || strings.HasPrefix(added.GID, "local:") {
		t.Errorf("time add --json = %q (%v)", stdout, err)
	}
	if _, errOut, err := run("time", "edit", added.GID, "--duration", "45m"); err != nil {
		t.Errorf("time edit with the printed gid: %v %s", err, errOut)
	}
	stdout, _, err = run("--json", "time", "list", "t1", "--offline")
	if err != nil {
		t.Fatal(err)
	}
	var es struct {
		Entries      []map[string]any `json:"entries"`
		TotalMinutes int              `json:"total_minutes"`
	}
	if err := json.Unmarshal([]byte(stdout), &es); err != nil || len(es.Entries) != 3 || es.TotalMinutes != 165 {
		t.Errorf("json %q (%v)", stdout, err)
	}
}

// Offline, edit and delete queue the change; delete needs --yes; a bad duration or day is
// refused with what to write.
func TestTimeEditDeleteOffline(t *testing.T) {
	path := writeCLIEnv(t)
	st, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	gid, err := st.AddTimeEntryLocal(context.Background(), "t1", 30, "2026-11-10", store.UserRef{GID: "me"}, time.Now())
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := runCLI(t, "time", "edit", gid, "--duration", "45m"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 1 || ops[0].Kind != store.OpAddTimeEntry || ops[0].Payload["duration_minutes"] != float64(45) {
		t.Errorf("ops %+v", ops)
	}
	if out, err := runCLI(t, "time", "delete", gid); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Errorf("delete without --yes: %v\n%s", err, out)
	}
	if out, err := runCLI(t, "time", "delete", gid, "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("ops %v", opKinds(ops))
	}
	if _, err := runCLI(t, "time", "add", "t1", "abc"); err == nil || !strings.Contains(err.Error(), "時間:") {
		t.Errorf("bad duration: %v", err)
	}
	if _, err := runCLI(t, "time", "add", "t1", "1h", "--date", "someday"); err == nil || !strings.Contains(err.Error(), "--date") {
		t.Errorf("bad date: %v", err)
	}
	if _, err := runCLI(t, "time", "edit", "te1"); err == nil {
		t.Error("edit without a change succeeded")
	}
}

// A 402 says that the plan lacks time tracking; from then on Asana is not asked and nothing
// is queued.
func TestTimePaymentRequired(t *testing.T) {
	path, fake, run := timeCLIEnv(t)
	fake.unpaid = true
	_, _, err := run("time", "list", "t1")
	if err == nil || !strings.Contains(err.Error(), "タイムトラッキング") {
		t.Fatalf("list = %v", err)
	}
	n := len(fake.took("GET /tasks"))
	if _, _, err := run("time", "list", "t1"); err == nil || len(fake.took("GET /tasks")) != n {
		t.Errorf("asked again: %v %q", err, fake.took(""))
	}
	if _, _, err := run("time", "add", "t1", "1h"); err == nil || !strings.Contains(err.Error(), "タイムトラッキング") {
		t.Errorf("add = %v", err)
	}
	if ops := cliOps(t, path); len(ops) != 0 {
		t.Errorf("queued %v", opKinds(ops))
	}
}
