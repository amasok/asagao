package richtext

// Target is the field rich text is sent to. Asana accepts a different set of tags in each
// (https://developers.asana.com/docs/rich-text): headings (<h1>, <h2>), rules (<hr/>),
// images (<img>) and tables are "supported in the rich text of only some objects", namely
// project briefs and tasks; everywhere else (stories, status updates, project notes,
// teams) only the general tags are, and "unsupported tags will be rejected with a
// 400 Bad Request error" (#63).
//
// FromMarkdown writes no tables, so what differs is headings, rules and images. Where they
// are not accepted, what the user wrote stays as text Asana accepts: a heading is a bold
// paragraph, a rule is the line as written ("---"), an image is its Markdown
// ("![画像](asana:GID)").
type Target int

const (
	// TaskNotes is a task's html_notes: headings, rules and images are written as such.
	TaskNotes Target = iota
	// Comment is a story's html_text.
	Comment
	// StatusUpdate is a project status update's html_text.
	StatusUpdate
)

// layout reports whether headings, rules and images are accepted in the target.
func (t Target) layout() bool { return t == TaskNotes }

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
