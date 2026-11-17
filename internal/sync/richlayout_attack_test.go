package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// Awkward spellings of headings, rules and images are all accepted by a fake Asana that
// rejects layout tags in html_text: none is left failed in the outbox (#63).
func TestPushNastyLayoutInputsNeverRejected(t *testing.T) {
	inputs := []string{
		"# 見出し", "#", "# ", "見出し\n===", "見出し\n---", "   ## 字下げ", "# **太** [l](https://x.jp) [@u](asana:user:9)",
		"# ![a](asana:1)", "![a\\]b [c]](asana:2)", "![](asana:3)", "![x](/home/u/.cache/asagao/attachments/55-a.png)",
		"---", "***", "___", "- - -", "* * *", "> # 引用の見出し\n> ---\n> ![i](asana:4)",
		"- # 項目\n- ---\n- ![i](asana:5)", "1. ## 番号\n   ***", "```\n# コード\n---\n```", "<h1>生の</h1><hr/><img src=x>",
		"# a & b < c > d", "\\# 文字", "# 長い " + string(make([]byte, 0)) + "見出し見出し見出し見出し見出し見出し",
	}
	ctx := context.Background()
	fake := &layoutAsana{sent: map[string]string{}}
	srv := httptest.NewServer(jsonReplies{fake, new(atomic.Int64)})
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Notes: "old", HTMLNotes: "<body>old</body>"}}); err != nil {
		t.Fatal(err)
	}
	var projects []asana.Project
	for i := range inputs {
		projects = append(projects, asana.Project{GID: fmt.Sprintf("p%d", i), Name: "P"})
	}
	if err := st.UpsertProjects(ctx, "w1", projects, "me"); err != nil {
		t.Fatal(err)
	}
	queued := 0
	for i, md := range inputs {
		if richtext.Comment.PlainFromMarkdown(md) == "" {
			continue // an empty heading: nothing to post
		}
		queued++
		if _, err := st.AddRichCommentLocal(ctx, "t1", richtext.Comment.PlainFromMarkdown(md), richtext.Comment.FromMarkdown(md), store.UserRef{GID: "me"}, now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateStatusLocal(ctx, fmt.Sprintf("p%d", i), "on_track", "週報"+md, richtext.StatusUpdate.PlainFromMarkdown(md), richtext.StatusUpdate.FromMarkdown(md), store.UserRef{GID: "me"}, now); err != nil {
			t.Fatal(err)
		}
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	var res PushResult
	for i := 0; i < 60; i++ {
		r, e := (&Syncer{API: api, Store: st}).Push(ctx)
		res.Sent += r.Sent
		res.Failed += r.Failed
		res.Remaining, err = r.Remaining, e
		if e != nil || r.Remaining == 0 {
			break
		}
	}
	if len(fake.rejected) > 0 {
		t.Fatalf("Asana would reject: %v", fake.rejected)
	}
	if err != nil {
		t.Fatalf("push: %+v %v", res, err)
	}
	ops, _ := st.ListOps(ctx)
	for _, op := range ops {
		if op.State == store.OpFailed {
			t.Fatalf("left failed: %+v", op)
		}
	}
	if res.Sent != 2*queued {
		for _, op := range ops {
			t.Logf("%s %s verify=%v %+v", op.Kind, op.State, op.Verify, op)
		}
		t.Fatalf("sent %d of %d: %+v err=%v", res.Sent, 2*queued, res, err)
	}
}

// jsonReplies re-encodes the fake's replies: layoutAsana writes html_text with %q, which is
// not always valid JSON (a title or text with an escape Go spells \x..).
type jsonReplies struct {
	f *layoutAsana
	n *atomic.Int64
}

func (j jsonReplies) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := httptest.NewRecorder()
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	j.f.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || r.Method == http.MethodGet {
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
		return
	}
	var in struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(body, &in)
	out := map[string]any{"gid": fmt.Sprint("s", j.n.Add(1)), "type": "comment", "text": "x", "html_text": in.Data["html_text"]}
	if r.URL.Path == "/status_updates" {
		out = map[string]any{"gid": fmt.Sprint("su", j.n.Add(1)), "status_type": "on_track", "title": in.Data["title"], "text": "x",
			"html_text": in.Data["html_text"], "author": map[string]any{"gid": "me"}, "created_at": "2026-11-16T00:00:00Z"}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": out})
}
