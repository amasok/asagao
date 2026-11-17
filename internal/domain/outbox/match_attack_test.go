package outbox

import (
	"html"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode"
)

// The pre-b6 implementations (sync/reconcile.go), kept to compare against.
var (
	oldMention = regexp.MustCompile(`(?s)<(a|img)\b[^>]*data-asana-gid[^>]*?(/>|>.*?</a>)`)
	oldTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	oldURL     = regexp.MustCompile(`https?://\S+`)
)

func oldStrip(s string) string {
	s = oldMention.ReplaceAllString(s, " ")
	return html.UnescapeString(oldTag.ReplaceAllString(s, " "))
}

func oldKey(s string) string {
	s = oldURL.ReplaceAllString(s, " ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func oldSameComment(stText, stHTML, text, htmlText string, clean func(string) string) bool {
	if oldKey(text) == "" {
		return strings.TrimSpace(clean(stText)) == strings.TrimSpace(clean(text))
	}
	if oldKey(stText) == oldKey(text) {
		return true
	}
	return htmlText != "" && stHTML != "" && oldKey(oldStrip(stHTML)) == oldKey(oldStrip(htmlText))
}

func oldSameFields(f TaskFields, payload map[string]any) bool {
	str := func(k string) string { v, _ := payload[k].(string); return v }
	if str("due_on") != f.DueOn || str("assignee") != f.AssigneeGID {
		return false
	}
	if _, rich := payload["html_notes"]; rich {
		return oldKey(oldStrip(f.HTMLNotes)) == oldKey(oldStrip(str("html_notes"))) || oldKey(f.Notes) == oldKey(str("notes"))
	}
	return oldKey(f.Notes) == oldKey(str("notes"))
}

func TestMatchSameAsBeforeOnRandomText(t *testing.T) {
	pieces := []string{"a", "B", "1", "漢字", "😀", " ", "\n", "-", "https://x.y/z", "http://", "<b>", "</b>", "<a data-asana-gid=\"1\">@u</a>",
		"<img data-asana-gid=\"2\"/>", "<a href=\"u\">", "</a>", "&amp;", "&lt;i&gt;", "&#x41;", "<", ">", "\x00", "​", "٣", "Ⅷ", "<body>", "</body>"}
	r := rand.New(rand.NewSource(1))
	gen := func() string {
		var b strings.Builder
		for n := r.Intn(6); n > 0; n-- {
			b.WriteString(pieces[r.Intn(len(pieces))])
		}
		return b.String()
	}
	clean := func(s string) string { return strings.ReplaceAll(s, "\x00", "") }
	for i := 0; i < 20000; i++ {
		a, b, c, d := gen(), gen(), gen(), gen()
		if i%3 == 0 { // make equal-ish pairs likely
			c, d = a, b
		}
		if g, w := SameComment(a, b, c, d, clean), oldSameComment(a, b, c, d, clean); g != w {
			t.Fatalf("SameComment(%q,%q,%q,%q) = %v, old %v", a, b, c, d, g, w)
		}
		if g, w := StripHTML(a), oldStrip(a); g != w {
			t.Fatalf("StripHTML(%q) = %q, old %q", a, g, w)
		}
		f := TaskFields{DueOn: []string{"", "2026-11-20"}[r.Intn(2)], AssigneeGID: []string{"", "u1"}[r.Intn(2)], Notes: a, HTMLNotes: b}
		p := map[string]any{"due_on": []string{"", "2026-11-20"}[r.Intn(2)], "assignee": []string{"", "u1"}[r.Intn(2)], "notes": c}
		if r.Intn(2) == 0 {
			p["html_notes"] = d
		}
		if r.Intn(5) == 0 {
			p["assignee"] = 5 // not a string
		}
		if g, w := SameTaskFields(f, p), oldSameFields(f, p); g != w {
			t.Fatalf("SameTaskFields(%+v, %v) = %v, old %v", f, p, g, w)
		}
	}
}
