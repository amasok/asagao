package rich

import "testing"

var (
	targets  = []Target{TaskNotes, Comment, StatusUpdate}
	elements = []Element{Heading, Rule, Image, Table, List, Quote, CodeBlock}
)

func TestTargetLayout(t *testing.T) {
	for _, tc := range []struct {
		target Target
		want   bool
	}{
		{TaskNotes, true},
		{Comment, false},
		{StatusUpdate, false},
	} {
		if got := tc.target.Layout(); got != tc.want {
			t.Errorf("Target(%d).Layout() = %v, want %v", tc.target, got, tc.want)
		}
	}
}

func TestElementLayout(t *testing.T) {
	for _, tc := range []struct {
		e    Element
		want bool
	}{
		{Heading, true},
		{Rule, true},
		{Image, true},
		{Table, true},
		{List, false},
		{Quote, false},
		{CodeBlock, false},
	} {
		if got := tc.e.Layout(); got != tc.want {
			t.Errorf("Element(%d).Layout() = %v, want %v", tc.e, got, tc.want)
		}
	}
}

// Task notes accept every element; a comment and a status update only those that are not
// layout (#63).
func TestAccepts(t *testing.T) {
	for _, tc := range []struct {
		target Target
		e      Element
		want   bool
	}{
		{TaskNotes, Heading, true},
		{TaskNotes, Rule, true},
		{TaskNotes, Image, true},
		{TaskNotes, Table, true},
		{TaskNotes, List, true},
		{TaskNotes, Quote, true},
		{TaskNotes, CodeBlock, true},
		{Comment, Heading, false},
		{Comment, Rule, false},
		{Comment, Image, false},
		{Comment, Table, false},
		{Comment, List, true},
		{Comment, Quote, true},
		{Comment, CodeBlock, true},
		{StatusUpdate, Heading, false},
		{StatusUpdate, Rule, false},
		{StatusUpdate, Image, false},
		{StatusUpdate, Table, false},
		{StatusUpdate, List, true},
		{StatusUpdate, Quote, true},
		{StatusUpdate, CodeBlock, true},
	} {
		if got := tc.target.Accepts(tc.e); got != tc.want {
			t.Errorf("Target(%d).Accepts(%d) = %v, want %v", tc.target, tc.e, got, tc.want)
		}
	}
}

func TestTagElement(t *testing.T) {
	for _, tc := range []struct {
		tag  string
		want Element
		ok   bool
	}{
		{"h1", Heading, true},
		{"h2", Heading, true},
		{"h3", Heading, true},
		{"h4", Heading, true},
		{"h5", Heading, true},
		{"h6", Heading, true},
		{"hr", Rule, true},
		{"img", Image, true},
		{"table", Table, true},
		{"ul", List, true},
		{"ol", List, true},
		{"blockquote", Quote, true},
		{"pre", CodeBlock, true},
		// general tags and others: not restricted
		{"", 0, false},
		{"h7", 0, false},
		{"h", 0, false},
		{"body", 0, false},
		{"strong", 0, false},
		{"em", 0, false},
		{"u", 0, false},
		{"s", 0, false},
		{"code", 0, false},
		{"a", 0, false},
		{"li", 0, false},
		{"tr", 0, false},
		{"td", 0, false},
		{"object", 0, false},
		{"div", 0, false},
		// the name is lower case, as the html parser gives it
		{"H1", 0, false},
		{"IMG", 0, false},
	} {
		got, ok := TagElement(tc.tag)
		if got != tc.want || ok != tc.ok {
			t.Errorf("TagElement(%q) = %d, %v, want %d, %v", tc.tag, got, ok, tc.want, tc.ok)
		}
	}
}

// Lists do not nest in a quote, and a quote, heading, code block or rule not in a list item
// (#60, #80). Everything else nests anywhere.
func TestNests(t *testing.T) {
	type where struct{ quote, item bool }
	top, inQuote, inItem, inBoth := where{}, where{quote: true}, where{item: true}, where{true, true}
	for _, tc := range []struct {
		e    Element
		in   where
		want bool
	}{
		{List, top, true},
		{List, inQuote, false},
		{List, inItem, true},
		{List, inBoth, false},
		{Quote, top, true},
		{Quote, inQuote, true},
		{Quote, inItem, false},
		{Quote, inBoth, false},
		{Heading, top, true},
		{Heading, inQuote, true},
		{Heading, inItem, false},
		{Heading, inBoth, false},
		{CodeBlock, top, true},
		{CodeBlock, inQuote, true},
		{CodeBlock, inItem, false},
		{CodeBlock, inBoth, false},
		{Rule, top, true},
		{Rule, inQuote, true},
		{Rule, inItem, false},
		{Rule, inBoth, false},
		{Image, top, true},
		{Image, inQuote, true},
		{Image, inItem, true},
		{Image, inBoth, true},
		{Table, top, true},
		{Table, inQuote, true},
		{Table, inItem, true},
		{Table, inBoth, true},
	} {
		if got := Nests(tc.e, tc.in.quote, tc.in.item); got != tc.want {
			t.Errorf("Nests(%d, quote=%v, item=%v) = %v, want %v", tc.e, tc.in.quote, tc.in.item, got, tc.want)
		}
	}
}

// Every element is accepted at the top level of some target, and Accepts agrees with
// Layout for each pair.
func TestAcceptsAgreesWithLayout(t *testing.T) {
	for _, tg := range targets {
		for _, e := range elements {
			if want := tg.Layout() || !e.Layout(); tg.Accepts(e) != want {
				t.Errorf("Target(%d).Accepts(%d) = %v, want %v", tg, e, !want, want)
			}
		}
	}
}
