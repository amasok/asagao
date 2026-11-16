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
	"testing"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
)

// asanaNestingError is what this fake Asana says about rich text it rejects: lists inside
// quotes, headings or code blocks, and those inside list items
// (https://developers.asana.com/docs/rich-text). "" means it is accepted.
func asanaNestingError(s string) string {
	s = regexp.MustCompile(`<a ([^>]*)/>`).ReplaceAllString(s, "<a $1></a>")
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return err.Error()
	}
	var walk func(n *html.Node, noList, inItem bool) string
	walk = func(n *html.Node, noList, inItem bool) string {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Ul, atom.Ol, atom.Li:
				if noList {
					return "list nested in a quote, header or pre"
				}
			case atom.H1, atom.H2, atom.Blockquote, atom.Pre:
				if inItem {
					return n.Data + " nested in a list item"
				}
				noList = true
			}
			if n.DataAtom == atom.Li {
				inItem = true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if e := walk(c, noList, inItem); e != "" {
				return e
			}
		}
		return ""
	}
	return walk(doc, false, false)
}

// Notes and a comment written in the editor with a list in a quote and a quote in a list
// are sent as rich text Asana accepts (#60), so the push succeeds and nothing is left
// failed in the outbox.
func TestPushNestedMarkdownIsAccepted(t *testing.T) {
	ctx := context.Background()
	var rejected []string
	sent := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"data":{"gid":"t1","name":"T","notes":"old","html_notes":"<body>old</body>"}}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(b, &body)
		for _, k := range []string{"html_notes", "html_text"} {
			v, ok := body.Data[k].(string)
			if !ok {
				continue
			}
			sent[k] = v
			if e := asanaNestingError(v); e != "" {
				rejected = append(rejected, k+": "+e)
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, `{"errors":[{"message":"%s: XML is invalid"}]}`, k)
				return
			}
		}
		if strings.HasSuffix(r.URL.Path, "/stories") {
			fmt.Fprintf(w, `{"data":{"gid":"s1","type":"comment","text":"x","html_text":%q}}`, sent["html_text"])
			return
		}
		fmt.Fprintf(w, `{"data":{"gid":"t1","name":"T","notes":"x","html_notes":%q}}`, sent["html_notes"])
	}))
	defer srv.Close()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "T", Notes: "old", HTMLNotes: "<body>old</body>"}}); err != nil {
		t.Fatal(err)
	}
	notes := "> 引用\n> - 項目1\n> - 項目2\n\n1. 手順\n   > 注意\n   ```\n   go test\n   ```\n2. 次"
	if err := st.UpdateTaskLocal(ctx, "t1", map[string]any{
		"notes": richtext.PlainFromMarkdown(notes), "html_notes": richtext.FromMarkdown(notes),
	}, now); err != nil {
		t.Fatal(err)
	}
	comment := "- > 引用した返事\n- # 見出し"
	if _, err := st.AddRichCommentLocal(ctx, "t1", richtext.PlainFromMarkdown(comment), richtext.FromMarkdown(comment), store.UserRef{GID: "me"}, now); err != nil {
		t.Fatal(err)
	}
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	res, err := (&Syncer{API: api, Store: st}).Push(ctx)
	if len(rejected) > 0 {
		t.Fatalf("Asana would reject: %v\nsent %q", rejected, sent)
	}
	if err != nil || res.Sent != 2 {
		t.Fatalf("push = %+v %v", res, err)
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
	for _, want := range []string{"項目1", "項目2", "注意", "go test", "次"} {
		if !strings.Contains(sent["html_notes"], want) {
			t.Errorf("html_notes lost %q: %q", want, sent["html_notes"])
		}
	}
	for _, want := range []string{"引用した返事", "見出し"} {
		if !strings.Contains(sent["html_text"], want) {
			t.Errorf("html_text lost %q: %q", want, sent["html_text"])
		}
	}
}
