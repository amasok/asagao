// Package rich holds Asana's rules for rich text that do not depend on how it is parsed or
// written: where it is sent (Target), which elements each target accepts, and which
// elements may nest in which. internal/richtext reads them when it converts Markdown.
package rich

// Target is the field rich text is sent to. Asana accepts a different set of tags in each
// (https://developers.asana.com/docs/rich-text): headings (<h1>, <h2>), rules (<hr/>),
// images (<img>) and tables are "supported in the rich text of only some objects", namely
// project briefs and tasks; everywhere else (stories, status updates, project notes,
// teams) only the general tags are, and "unsupported tags will be rejected with a
// 400 Bad Request error" (#63).
type Target int

const (
	// TaskNotes is a task's html_notes: headings, rules and images are written as such.
	TaskNotes Target = iota
	// Comment is a story's html_text.
	Comment
	// StatusUpdate is a project status update's html_text.
	StatusUpdate
)

// Layout reports whether t accepts the layout elements (see Element.Layout).
func (t Target) Layout() bool { return t == TaskNotes }

// Accepts reports whether t accepts element e.
func (t Target) Accepts(e Element) bool { return t.Layout() || !e.Layout() }

// Element is a kind of block or image in rich text whose place is restricted: by the
// target (Target.Accepts) or by what holds it (Nests).
type Element int

const (
	Heading   Element = iota // <h1>, <h2> (Asana's rich text has no other level)
	Rule                     // <hr/>
	Image                    // <img>
	Table                    // <table>
	List                     // <ul>, <ol>
	Quote                    // <blockquote>
	CodeBlock                // <pre>
)

// Layout reports whether e is accepted only in task notes (and project briefs): a heading,
// a rule, an image or a table.
func (e Element) Layout() bool {
	switch e {
	case Heading, Rule, Image, Table:
		return true
	}
	return false
}

// TagElement returns the element an html tag (its lower-case name) writes, false for a tag
// that is none of them. All six heading levels count as Heading, since none is accepted
// outside task notes.
func TagElement(tag string) (Element, bool) {
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return Heading, true
	case "hr":
		return Rule, true
	case "img":
		return Image, true
	case "table":
		return Table, true
	case "ul", "ol":
		return List, true
	case "blockquote":
		return Quote, true
	case "pre":
		return CodeBlock, true
	}
	return 0, false
}

// Nests reports whether element e may be written as an element inside a quote (inQuote) or
// a list item (inItem). Asana's rich text does not let lists nest inside a quote, nor a
// quote, heading, code block or rule inside a list item, and rejects such html_text with
// 400 (#60; a rule too: the documentation does not say an item takes <hr/>, #80). Where it
// may not, the Markdown for it is kept as text instead.
func Nests(e Element, inQuote, inItem bool) bool {
	switch e {
	case List:
		return !inQuote
	case Quote, Heading, CodeBlock, Rule:
		return !inItem
	}
	return true
}
