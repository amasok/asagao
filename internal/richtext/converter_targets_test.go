package richtext

import "testing"

// Each method of Converter converts for its own target: a heading, a rule and an image are
// kept as such only in task notes, so the output for a comment or a status update is not
// the task notes' one.
func TestConverterCommentAndStatusUpdate(t *testing.T) {
	src := "# 見出し\n\n本文 **太字**\n\n---\n\n![図](asana:42)"
	for _, tc := range []struct {
		name   string
		target Target
		conv   func(string) (string, string)
	}{
		{"Comment", Comment, Converter{}.Comment},
		{"StatusUpdate", StatusUpdate, Converter{}.StatusUpdate},
	} {
		t.Run(tc.name, func(t *testing.T) {
			html, plain := tc.conv(src)
			if want := tc.target.FromMarkdown(src); html != want {
				t.Errorf("html = %q, want %q", html, want)
			}
			if want := tc.target.PlainFromMarkdown(src); plain != want {
				t.Errorf("plain = %q, want %q", plain, want)
			}
			if tn := TaskNotes.FromMarkdown(src); html == tn {
				t.Errorf("html is the task notes' one: %q", html)
			}
			if html == plain {
				t.Errorf("html and plain are the same: %q", html)
			}
		})
	}
}
