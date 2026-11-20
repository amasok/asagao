package tui

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/asana"
)

// realNotesHTML is the shape of html_notes Asana returns for a pasted screenshot (signed URL).
const realNotesHTML = `<body>説明です<img src="https://asanausercontent.com/us1/assets/1200000000000000/1220507679870013/abcdef0123456789?e=1763100000&amp;v=0&amp;t=signature" data-asana-gid="1220507679870013" data-asana-type="attachment" data-src-height="230" data-src-width="406" data-thumbnail-url="https://asanausercontent.com/us1/assets/1200000000000000/1220507679870013/thumb?e=1&amp;v=0&amp;t=x" alt="スクリーンショット 2026-11-13 15.53.03.png" style="display:block;max-width:100%;margin-left:auto;margin-right:auto" />後ろの文</body>`

func TestInlineImageFromSignedURL(t *testing.T) {
	st := fixtureStore(t) // also points XDG_CACHE_HOME at a temporary directory
	cache := os.Getenv("XDG_CACHE_HOME")
	ctx := context.Background()
	name := "スクリーンショット 2026-11-13 15.53.03.png"
	path := filepath.Join(cache, "asagao", "attachments", "1220507679870013-"+name)
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, pngBytes(t, 406, 230), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20",
		HTMLNotes: realNotesHTML,
		Assignee:  &asana.Ref{GID: "me"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpsertAttachment(ctx, "t1", asana.Attachment{GID: "1220507679870013", Name: name})
	_ = st.SetAttachmentPath(ctx, "1220507679870013", path)

	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }, InlineImages: true})
	m.cellW, m.cellH = 10, 20
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	h.run(m.Init())
	h.key("h", "j", "j", "l")
	out := h.m.render()
	if !strings.Contains(out, "\U0010EEEE") {
		t.Fatalf("detail should contain kitty placeholders: inline=%+v status=%q\n%s", h.m.inline["1220507679870013"], h.m.status, ansi.Strip(out))
	}
	h.key("h", "enter")
	if out := h.m.render(); !strings.Contains(out, "\U0010EEEE") {
		t.Fatalf("full-screen detail should contain kitty placeholders: inline=%+v\n%s", h.m.inline["1220507679870013"], ansi.Strip(out))
	}
}

// A failed load says why on the image line and in the status bar, instead of looking like
// inline images are off.
func TestInlineImageFailureReason(t *testing.T) {
	st := fixtureStore(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "1220507679870013-shot.png")
	if err := os.WriteFile(path, []byte("<html>expired</html>"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "ソファを買う", DueOn: "2026-11-20",
		HTMLNotes: realNotesHTML,
		Assignee:  &asana.Ref{GID: "me"}, Memberships: []asana.Membership{{Project: &asana.Ref{GID: "p1"}, Section: &asana.Ref{GID: "s1"}}}}}); err != nil {
		t.Fatal(err)
	}
	_ = st.UpsertAttachment(ctx, "t1", asana.Attachment{GID: "1220507679870013", Name: "shot.png"})
	_ = st.SetAttachmentPath(ctx, "1220507679870013", path)

	m := New(ctx, Deps{Store: st, Now: func() time.Time { return testNow }, InlineImages: true})
	h := &harness{t: t, m: m}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	h.run(m.Init())
	h.key("h", "j", "j", "l")
	s := h.screen()
	if !strings.Contains(s, "[画像1]（表示できません: 画像として読") {
		t.Fatalf("image line should give the reason:\n%s", s)
	}
	h.send(detailLoadedMsg{gid: "t1", d: h.m.detail.task}) // the task is loaded again: tries again, fails again
	if !strings.Contains(h.m.status, "画像1 を表示できません: 画像として読めません（1220507679870013-shot.png）") {
		t.Fatalf("status = %q", h.m.status)
	}
	// Opening the task again tries again.
	_ = os.WriteFile(path, pngBytes(t, 40, 20), 0o600)
	h.send(detailLoadedMsg{gid: "t1", d: h.m.detail.task})
	if !strings.Contains(h.m.render(), "\U0010EEEE") {
		t.Fatalf("image should load on retry: %+v\n%s", h.m.inline["1220507679870013"], h.screen())
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
