package outbox

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

// Verifying an entry whose result is unknown looks in Asana for what it would have
// created (see sync/reconcile.go). These are the conditions under which something found
// there is taken for what the entry sent.

// TaskFields are the fields of a task in Asana that a create sets besides the name.
type TaskFields struct {
	DueOn       string
	StartOn     string
	AssigneeGID string // "" for none
	Notes       string
	HTMLNotes   string
}

// SameTaskFields compares the fields a create (its payload) sets besides the name, so a
// task someone made by hand with the same name is not taken for ours. Descriptions are
// compared by their visible words (TextKey); a rich one matches by its html_notes or its
// notes.
func SameTaskFields(t TaskFields, payload map[string]any) bool {
	str := func(k string) string { v, _ := payload[k].(string); return v }
	if str("due_on") != t.DueOn || str("start_on") != t.StartOn {
		return false
	}
	if str("assignee") != t.AssigneeGID {
		return false
	}
	if _, rich := payload["html_notes"]; rich {
		return TextKey(StripHTML(t.HTMLNotes)) == TextKey(StripHTML(str("html_notes"))) ||
			TextKey(t.Notes) == TextKey(str("notes"))
	}
	return TextKey(t.Notes) == TextKey(str("notes"))
}

// SameComment reports whether a story (storyText, storyHTML) is the comment we sent
// (text, htmlText). Asana makes the plain text of a rich-text comment itself (links,
// mentions and lists come out differently from our Markdown-derived text), so the visible
// words of either form are compared. A comment with no letters or digits (only emoji or
// symbols) has nothing to compare loosely: it must be the same text, after clean (the
// terminal-safe form, termsafe.String, which domain code does not import) and trimming.
func SameComment(storyText, storyHTML, text, htmlText string, clean func(string) string) bool {
	if TextKey(text) == "" {
		return strings.TrimSpace(clean(storyText)) == strings.TrimSpace(clean(text))
	}
	if TextKey(storyText) == TextKey(text) {
		return true
	}
	return htmlText != "" && storyHTML != "" && TextKey(StripHTML(storyHTML)) == TextKey(StripHTML(htmlText))
}

var (
	// mentionOrImage matches <a>/<img> elements Asana fills in itself from data-asana-gid
	// (we send them empty; Asana returns the user's name or the image).
	mentionOrImage = regexp.MustCompile(`(?s)<(a|img)\b[^>]*data-asana-gid[^>]*?(/>|>.*?</a>)`)
	tagRE          = regexp.MustCompile(`(?s)<[^>]*>`)
	urlRE          = regexp.MustCompile(`https?://\S+`)
)

// StripHTML returns the visible text of Asana rich text, without mentions and images.
func StripHTML(s string) string {
	s = mentionOrImage.ReplaceAllString(s, " ")
	return html.UnescapeString(tagRE.ReplaceAllString(s, " "))
}

// TextKey keeps only letters and digits (URLs removed), so spacing, punctuation, list
// markers and link targets do not matter.
func TextKey(s string) string {
	s = urlRE.ReplaceAllString(s, " ")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
