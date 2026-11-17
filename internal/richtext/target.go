package richtext

import (
	"strings"

	"golang.org/x/net/html"

	"github.com/amasok/asagao/internal/domain/rich"
)

// Target is the field rich text is sent to (rich.Target, where Asana's rules for each are).
//
// FromMarkdown writes no tables, so what differs is headings, rules and images. Where they
// are not accepted, what the user wrote stays as text Asana accepts: a heading is a bold
// paragraph, a rule is the line as written ("---"), an image is its Markdown
// ("![画像](asana:GID)").
type Target rich.Target

const (
	// TaskNotes is a task's html_notes: headings, rules and images are written as such.
	TaskNotes = Target(rich.TaskNotes)
	// Comment is a story's html_text.
	Comment = Target(rich.Comment)
	// StatusUpdate is a project status update's html_text.
	StatusUpdate = Target(rich.StatusUpdate)
)

// accepts reports whether element e is accepted in the target.
func (t Target) accepts(e rich.Element) bool { return rich.Target(t).Accepts(e) }

// FromMarkdown converts Markdown to Asana rich text for the target, wrapped in <body>.
func (t Target) FromMarkdown(src string) string { return fromMarkdown(src, t) }

// PlainFromMarkdown returns the text of Markdown without markup, as FromMarkdown writes it
// for the target (for Asana's plain notes / text).
func (t Target) PlainFromMarkdown(src string) string { return plainFromMarkdown(src, t) }

// Converter converts Markdown to Asana rich text, one method per target. It satisfies
// app.RichText, so that internal/app does not depend on Target.
type Converter struct{}

// TaskNotes converts a task description to html_notes and its plain notes.
func (Converter) TaskNotes(md string) (html, plain string) {
	return TaskNotes.FromMarkdown(md), TaskNotes.PlainFromMarkdown(md)
}

// Comment converts a comment to a story's html_text and its plain text.
func (Converter) Comment(md string) (html, plain string) {
	return Comment.FromMarkdown(md), Comment.PlainFromMarkdown(md)
}

// StatusUpdate converts the body of a status update to its html_text and plain text.
func (Converter) StatusUpdate(md string) (html, plain string) {
	return StatusUpdate.FromMarkdown(md), StatusUpdate.PlainFromMarkdown(md)
}

// Fit is Target(t).Fit, for the sync engine and the store, which name targets by
// domain/rich.
func (Converter) Fit(t rich.Target, htmlText string) (fitted, plain string, changed bool) {
	return Target(t).Fit(htmlText)
}

// Fit returns rich text html as it is written for the target now, and whether it changed.
// Rich text queued for a comment or a status update before #63 may hold headings, rules or
// images, which Asana rejects there with 400 (#71): it is read back as editable Markdown
// and converted for the target again, so what was written stays as text, as FromMarkdown
// writes it now. html with no such tag, or for a target that accepts them, is kept as it is.
func (t Target) Fit(htmlText string) (fitted, plain string, changed bool) {
	if rich.Target(t).Layout() || !hasLayoutTag(htmlText) {
		return htmlText, "", false
	}
	md := ToEditableMarkdown(htmlText)
	return t.FromMarkdown(md), t.PlainFromMarkdown(md), true
}

// hasLayoutTag reports whether s has an element accepted only in task notes (a heading, a
// rule, an image or a table).
func hasLayoutTag(s string) bool {
	if !strings.Contains(s, "<") {
		return false
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return false
	}
	var found func(n *html.Node) bool
	found = func(n *html.Node) bool {
		if n.Type == html.ElementNode {
			if e, ok := rich.TagElement(n.DataAtom.String()); ok && e.Layout() {
				return true
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if found(c) {
				return true
			}
		}
		return false
	}
	return found(doc)
}
