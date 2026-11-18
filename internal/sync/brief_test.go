package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// briefServer is Asana with one project (p1, in workspace w1) and its brief (#60).
// Assumed of the preview API (not documented, checked on the real one later): GET
// /projects/{gid} with opt_fields=project_brief gives {"gid"} or null; POST answers the new
// brief with its gid; PUT takes title and html_text and answers the brief; DELETE answers
// {"data":{}}; text is the brief's plain text.
type briefServer struct {
	t  *testing.T
	mu gosync.Mutex
	// the brief as Asana has it ("" gid: none)
	gid, title, text, html string
	status                 map[string]int // "METHOD path" → status to answer instead
	bodies                 map[string][]map[string]any
	calls                  map[string]int
	nextGID                int
}

func (b *briefServer) briefJSON() string {
	return fmt.Sprintf(`{"gid":%q,"title":%s,"text":%s,"html_text":%s}`, b.gid, jsonStr(b.title), jsonStr(b.text), jsonStr(b.html))
}

func (b *briefServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := r.Method + " " + r.URL.Path
	b.calls[key]++
	if st := b.status[key]; st != 0 {
		w.WriteHeader(st)
		fmt.Fprint(w, `{"errors":[{"message":"nope"}]}`)
		return
	}
	var body struct{ Data map[string]any }
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Data != nil {
		b.bodies[key] = append(b.bodies[key], body.Data)
	}
	str := func(k string) string { s, _ := body.Data[k].(string); return s }
	path := "/project_briefs/" + b.gid
	switch {
	case key == "GET /users/me":
		fmt.Fprint(w, `{"data":{"gid":"me","name":"私"}}`)
	case key == "GET /projects/p1":
		ref := "null"
		if b.gid != "" {
			ref = fmt.Sprintf(`{"gid":%q}`, b.gid)
		}
		fmt.Fprintf(w, `{"data":{"gid":"p1","name":"新居","workspace":{"gid":"w1"},"members":[{"gid":"me"}],"project_brief":%s}}`, ref)
	case key == "GET /projects/p1/task_counts":
		fmt.Fprint(w, `{"data":{"num_tasks":1}}`)
	case key == "POST /projects/p1/project_briefs":
		if b.gid != "" {
			w.WriteHeader(http.StatusBadRequest) // a project has one brief
			fmt.Fprint(w, `{"errors":[{"message":"already has a brief"}]}`)
			return
		}
		if _, both := body.Data["text"]; both {
			b.t.Errorf("text sent with html_text: %v", body.Data)
		}
		b.nextGID++
		b.gid, b.title, b.html = fmt.Sprintf("b%d", b.nextGID), str("title"), str("html_text")
		b.text = plainOf(b.html)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintf(w, `{"data":%s}`, b.briefJSON())
	case b.gid != "" && key == "GET "+path:
		fmt.Fprintf(w, `{"data":%s}`, b.briefJSON())
	case b.gid != "" && key == "PUT "+path:
		if _, both := body.Data["text"]; both {
			b.t.Errorf("text sent with html_text: %v", body.Data)
		}
		b.title, b.html = str("title"), str("html_text")
		b.text = plainOf(b.html)
		fmt.Fprintf(w, `{"data":%s}`, b.briefJSON())
	case b.gid != "" && key == "DELETE "+path:
		b.gid, b.title, b.text, b.html = "", "", "", ""
		fmt.Fprint(w, `{"data":{}}`)
	case r.Method != "POST" && len(r.URL.Path) > len("/project_briefs/") && r.URL.Path[:len("/project_briefs/")] == "/project_briefs/":
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errors":[{"message":"not found"}]}`)
	default:
		b.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(http.StatusTeapot)
	}
}

// plainOf is the text of rich text, as Asana gives it (tags dropped).
func plainOf(html string) string {
	out, in := []rune{}, false
	for _, r := range html {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			out = append(out, r)
		}
	}
	return string(out)
}

func setupBrief(t *testing.T, title, text string) (*briefServer, *Syncer, *store.Store) {
	t.Helper()
	bs := &briefServer{t: t, status: map[string]int{}, bodies: map[string][]map[string]any{}, calls: map[string]int{}, nextGID: 1}
	if title != "" || text != "" {
		bs.gid, bs.title, bs.text, bs.html = "b1", title, text, "<body>"+text+"</body>"
	}
	srv := httptest.NewServer(bs)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL, api.MaxRetries = srv.URL, 0
	s := &Syncer{API: api, Store: st, now: func() time.Time { return now }}
	_ = st.SetSyncState(ctx, store.KeyMeGID, "me")
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "会社", IsOrganization: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	return bs, s, st
}

func edit(title, text string) store.BriefEdit {
	return store.BriefEdit{Title: title, HTMLText: "<body>" + text + "</body>", Text: text}
}

func briefOps(t *testing.T, st *store.Store) []store.Op {
	t.Helper()
	ops, err := st.ListOps(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ops
}

// A brief made here is created with POST /projects/{gid}/project_briefs (title and
// html_text, not text), and the cache then has Asana's.
func TestPushCreateBrief(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "", "")
	if o, _ := st.ProjectOverview(ctx, "p1"); o.BriefState != store.BriefNone {
		t.Fatalf("state %q", o.BriefState)
	}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "年内に引っ越す"), now); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Title != "目的" || !o.BriefPending {
		t.Fatalf("local brief = %+v pending=%v", o.Brief, o.BriefPending)
	}
	res, err := s.Push(ctx)
	if err != nil || res.Sent != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if bs.gid != "b2" || bs.title != "目的" || bs.html != "<body>年内に引っ越す</body>" {
		t.Fatalf("asana brief = %+v", bs)
	}
	if ops := briefOps(t, st); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief == nil || o.BriefPending || o.Brief.Text != "年内に引っ越す" {
		t.Fatalf("cache after = %+v", o)
	}
}

// A change is sent with PUT /project_briefs/{gid}; a delete with DELETE.
func TestPushUpdateAndDeleteBrief(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内に引っ越す")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春までに引っ越す"), now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.text != "春までに引っ越す" {
		t.Fatalf("push = %+v %v text=%q", res, err, bs.text)
	}
	if n := len(bs.bodies["PUT /project_briefs/b1"]); n != 1 {
		t.Fatalf("puts = %d", n)
	}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", store.BriefEdit{Delete: true}, now); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief != nil || o.BriefState != store.BriefNone {
		t.Fatalf("local after delete = %+v", o)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.gid != "" || bs.calls["DELETE /project_briefs/b1"] != 1 {
		t.Fatalf("push = %+v %v gid=%q", res, err, bs.gid)
	}
}

// A change only in formatting (the same plain text) is still sent.
func TestPushBriefFormattingOnly(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内に引っ越す")
	e := store.BriefEdit{Title: "目的", HTMLText: "<body><strong>年内に引っ越す</strong></body>", Text: "年内に引っ越す"}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", e, now); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.html != e.HTMLText {
		t.Fatalf("push = %+v %v html=%q", res, err, bs.html)
	}
}

// A brief changed in Asana since it was edited here is a conflict, not overwritten; keeping
// mine sends it on the next push. Changed there to the same thing is no conflict.
func TestPushBriefConflict(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内に引っ越す")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春までに"), now); err != nil {
		t.Fatal(err)
	}
	bs.text, bs.html = "夏までに", "<body>夏までに</body>" // changed there too
	res, err := s.Push(ctx)
	if err != nil || res.Conflicts != 1 || len(bs.bodies["PUT /project_briefs/b1"]) != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	ops := briefOps(t, st)
	if len(ops) != 1 || ops[0].Conflict["text"] != "夏までに" || ops[0].Conflict["state"] != store.BriefAvailable {
		t.Fatalf("conflict = %+v", ops)
	}
	if lines := readmodel.ConflictLines(ops[0]); len(lines) != 1 || lines[0] != "ブリーフ: こちら=春までに / Asana=夏までに" {
		t.Fatalf("lines = %q", lines)
	}
	// another edit while in conflict folds into the same entry, still in conflict
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "秋までに"), now); err != nil {
		t.Fatal(err)
	}
	if ops := briefOps(t, st); len(ops) != 1 || ops[0].State != store.OpConflict || ops[0].Payload["text"] != "秋までに" {
		t.Fatalf("after edit in conflict = %+v", ops)
	}
	if err := st.ResolveKeepLocal(ctx, ops[0].ID); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.text != "秋までに" {
		t.Fatalf("after keep local: %+v %v text=%q", res, err, bs.text)
	}

	// the same change made there: done, nothing in conflict
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "冬までに"), now); err != nil {
		t.Fatal(err)
	}
	bs.text, bs.html = "冬までに", "<body>冬までに</body>"
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 0 || res.Sent != 1 {
		t.Fatalf("same change: %+v %v", res, err)
	}
}

// A brief deleted in Asana while changed here, or made there while one was made here, is
// a conflict; keeping mine then makes it again / overwrites it.
func TestPushBriefConflictDeletedOrMadeThere(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "目的", "年内に引っ越す")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春までに"), now); err != nil {
		t.Fatal(err)
	}
	bs.gid, bs.title, bs.text, bs.html = "", "", "", ""
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("deleted there: %+v %v", res, err)
	}
	ops := briefOps(t, st)
	if ops[0].Conflict["state"] != store.BriefNone {
		t.Fatalf("conflict = %+v", ops[0].Conflict)
	}
	_ = st.ResolveKeepLocal(ctx, ops[0].ID)
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.gid == "" || bs.text != "春までに" {
		t.Fatalf("keep local after delete: %+v %v %+v", res, err, bs)
	}

	// made here while Asana got one
	bs2, s2, st2 := setupBrief(t, "", "")
	if err := st2.UpdateProjectBriefLocal(ctx, "p1", edit("mine", "こちら"), now); err != nil {
		t.Fatal(err)
	}
	bs2.gid, bs2.title, bs2.text, bs2.html = "b9", "theirs", "あちら", "<body>あちら</body>"
	if res, err := s2.Push(ctx); err != nil || res.Conflicts != 1 || bs2.calls["POST /projects/p1/project_briefs"] != 0 {
		t.Fatalf("made there: %+v %v", res, err)
	}
	ops = briefOps(t, st2)
	_ = st2.ResolveKeepLocal(ctx, ops[0].ID)
	if res, err := s2.Push(ctx); err != nil || res.Sent != 1 || bs2.text != "こちら" || bs2.gid != "b9" {
		t.Fatalf("keep local over theirs: %+v %v %+v", res, err, bs2)
	}
}

// A create whose answer was lost (Asana made it) is not made again: found by its text.
func TestPushCreateBriefAnswerLost(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "", "")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "年内"), now); err != nil {
		t.Fatal(err)
	}
	// as if sent: Asana has it
	bs.gid, bs.title, bs.text, bs.html = "b5", "目的", "年内", "<body>年内</body>"
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || bs.calls["POST /projects/p1/project_briefs"] != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
	if ops := briefOps(t, st); len(ops) != 0 {
		t.Fatalf("left %+v", ops)
	}
}

// 402 or 403 for the brief fails the change with a message that says so, and marks the
// workspace (not other workspaces) so that the brief is not offered there for a while.
func TestPushBriefRefused(t *testing.T) {
	for _, status := range []int{http.StatusPaymentRequired, http.StatusForbidden} {
		ctx := context.Background()
		bs, s, st := setupBrief(t, "目的", "年内")
		if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "春"), now); err != nil {
			t.Fatal(err)
		}
		bs.status["PUT /project_briefs/b1"] = status
		res, err := s.Push(ctx)
		if err != nil || res.Failed != 1 {
			t.Fatalf("%d: push = %+v %v", status, res, err)
		}
		ops := briefOps(t, st)
		if len(ops) != 1 || ops[0].State != store.OpFailed {
			t.Fatalf("%d: ops = %+v", status, ops)
		}
		v, _ := st.SyncState(ctx, readmodel.PlanKey(readmodel.PlanProjectBrief, "w1"))
		if !readmodel.PlanOffFor(v, now) {
			t.Fatalf("%d: workspace not marked (%q)", status, v)
		}
		if v, _ := st.SyncState(ctx, readmodel.KeyPlanUnavailable+readmodel.PlanProjectBrief); v != "" {
			t.Fatalf("%d: marked for every workspace", status)
		}
	}
}

// Fetching the overview keeps a brief change not sent yet.
func TestFetchOverviewKeepsUnsentBrief(t *testing.T) {
	ctx := context.Background()
	_, s, st := setupBrief(t, "目的", "年内")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("新しい", "春"), now); err != nil {
		t.Fatal(err)
	}
	if err := s.FetchProjectOverview(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief == nil || o.Brief.Title != "新しい" || o.Brief.Text != "春" {
		t.Fatalf("brief = %+v", o.Brief)
	}
	if err := st.UpdateProjectBriefLocal(ctx, "p1", store.BriefEdit{Delete: true}, now); err != nil {
		t.Fatal(err)
	}
	_ = s.FetchProjectOverview(ctx, "p1")
	if o, _ := st.ProjectOverview(ctx, "p1"); o.Brief != nil {
		t.Fatalf("deleted brief back: %+v", o.Brief)
	}
}

// A create sent before (its answer lost) whose brief Asana writes with another text (a
// table, a list) is still found by its title: not a conflict (review of #60).
func TestPushCreateBriefSentBeforeFoundByTitle(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "", "")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "- 年内"), now); err != nil {
		t.Fatal(err)
	}
	ops := briefOps(t, st)
	if _, err := st.BeginSend(ctx, ops[0].ID, now); err != nil { // as if sent once, its answer lost
		t.Fatal(err)
	}
	_ = st.MarkOpVerify(ctx, ops[0].ID, "")
	bs.gid, bs.title, bs.text, bs.html = "b5", "目的", "年内", "<body><ul><li>年内</li></ul></body>"
	if res, err := s.Push(ctx); err != nil || res.Sent != 1 || res.Conflicts != 0 || bs.calls["POST /projects/p1/project_briefs"] != 0 {
		t.Fatalf("push = %+v %v", res, err)
	}
}

// A brief made in Asana with the same title, while one made here was never sent, is a
// conflict: only an entry sent before (verify) is matched by its title alone.
func TestPushCreateBriefNotSentSameTitleConflicts(t *testing.T) {
	ctx := context.Background()
	bs, s, st := setupBrief(t, "", "")
	if err := st.UpdateProjectBriefLocal(ctx, "p1", edit("目的", "こちら"), now); err != nil {
		t.Fatal(err)
	}
	bs.gid, bs.title, bs.text, bs.html = "b5", "目的", "あちら", "<body>あちら</body>"
	if res, err := s.Push(ctx); err != nil || res.Conflicts != 1 {
		t.Fatalf("push = %+v %v", res, err)
	}
}
