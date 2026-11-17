package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	gosync "sync"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// layoutTag matches the tags Asana accepts only in tasks and project briefs: headings,
// rules, images and tables (https://developers.asana.com/docs/rich-text).
var layoutTag = regexp.MustCompile(`(?i)<(h[1-6]|hr|img|table|tr|td|object)\b`)

// layoutAsana is a fake Asana that, like Asana, rejects with 400 a comment or a status
// update whose html_text has a heading, a rule or an image, and takes them in a task's
// html_notes. It records what it was sent and what it rejected.
type layoutAsana struct {
	mu       gosync.Mutex
	sent     map[string]string // "POST /tasks/t1/stories html_text" -> the rich text
	rejected []string
	created  int // gids of what it creates: s1, su2, ...
}

func (f *layoutAsana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Method == http.MethodGet {
		fmt.Fprint(w, `{"data":{"gid":"t1","name":"T","notes":"old","html_notes":"<body>old</body>"}}`)
		return
	}
	b, _ := io.ReadAll(r.Body)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(b, &body)
	req := r.Method + " " + r.URL.Path
	for _, k := range []string{"html_notes", "html_text"} {
		v, ok := body.Data[k].(string)
		if !ok {
			continue
		}
		f.sent[req+" "+k] = v
		if m := layoutTag.FindString(v); k == "html_text" && m != "" {
			f.rejected = append(f.rejected, req+": "+m+">")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"errors":[{"message":"%s: XML is invalid"}]}`, k)
			return
		}
	}
	f.created++
	switch {
	case strings.HasSuffix(r.URL.Path, "/stories"):
		fmt.Fprintf(w, `{"data":{"gid":"s%d","type":"comment","text":"x","html_text":%q}}`, f.created, body.Data["html_text"])
	case r.URL.Path == "/status_updates":
		fmt.Fprintf(w, `{"data":{"gid":"su%d","status_type":"on_track","title":"t","text":"x","html_text":%q,"author":{"gid":"me"},"created_at":"2026-11-16T00:00:00Z"}}`, f.created, body.Data["html_text"])
	default:
		fmt.Fprintf(w, `{"data":{"gid":"t1","name":"T","notes":"x","html_notes":%q}}`, body.Data["html_notes"])
	}
}

// Notes, a comment and a status update written with headings, a rule and an image are all
// sent (#63): Asana takes those only in a task's notes and rejects them with 400 in a
// comment or a status update, where they are sent as text Asana takes, so nothing is left
// failed in the outbox and no word is lost.
func TestPushHeadingsRulesImagesOnlyInTaskNotes(t *testing.T) {
	ctx := context.Background()
	fake := &layoutAsana{sent: map[string]string{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Notes: "old", HTMLNotes: "<body>old</body>"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{{GID: "p1", Name: "P"}}, "me"); err != nil {
		t.Fatal(err)
	}
	md := "# 予定\n\n明日やる\n\n---\n\n## 準備\n\n![図](asana:42)\n\n***"
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{
		"notes": richtext.TaskNotes.PlainFromMarkdown(md), "html_notes": richtext.TaskNotes.FromMarkdown(md),
	}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddRichCommentLocal(ctx, "t1", richtext.Comment.PlainFromMarkdown(md), richtext.Comment.FromMarkdown(md), store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateStatusLocal(ctx, "p1", "on_track", "週報", richtext.StatusUpdate.PlainFromMarkdown(md), richtext.StatusUpdate.FromMarkdown(md), store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if len(fake.rejected) > 0 {
		t.Fatalf("Asana would reject: %v\nsent %q", fake.rejected, fake.sent)
	}
	if err != nil || res.Sent != 3 {
		t.Fatalf("push = %+v %v (sent %q)", res, err, fake.sent)
	}
	ops, err := st.ListOps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.State == store.OpFailed {
			t.Fatalf("left failed: %+v", op)
		}
	}
	notes := fake.sent["PUT /tasks/t1 html_notes"]
	for _, want := range []string{"<h1>予定</h1>", "<h2>準備</h2>", "<hr/>", `<img data-asana-gid="42"/>`} {
		if !strings.Contains(notes, want) {
			t.Errorf("html_notes should keep %s: %q", want, notes)
		}
	}
	for _, k := range []string{"POST /tasks/t1/stories html_text", "POST /status_updates html_text"} {
		for _, want := range []string{"<strong>予定</strong>", "明日やる", "---", "<strong>準備</strong>", "![図](asana:42)", "***"} {
			if !strings.Contains(fake.sent[k], want) {
				t.Errorf("%s lost %q: %q", k, want, fake.sent[k])
			}
		}
	}
}
