package store

import (
	"reflect"
	"testing"
)

// DiscardDetails converts rich text with the function it is given, and only rich text; when
// that gives nothing but blanks, the plain text is shown instead.
func TestDiscardDetailsConvertsWithTheGivenFunction(t *testing.T) {
	var seen []string
	toMarkdown := func(h string) string {
		seen = append(seen, h)
		switch h {
		case "<body>blank</body>":
			return " \n\t"
		case "<body>empty</body>":
			return ""
		}
		return "  md(" + h + ")\n"
	}
	for _, tc := range []struct {
		name string
		d    Discard
		want []DiscardDetail
		seen []string
	}{
		{"comment", Discard{Kind: OpAddComment, Payload: map[string]any{"text": "平文", "html_text": "<body>c</body>"}},
			[]DiscardDetail{{"コメント（Markdown）", "md(<body>c</body>)"}}, []string{"<body>c</body>"}},
		{"status", Discard{Kind: OpCreateStatus, Payload: map[string]any{"title": "週報", "text": "平文", "html_text": "<body>s</body>"}},
			[]DiscardDetail{{"タイトル", "週報"}, {"本文（Markdown）", "md(<body>s</body>)"}}, []string{"<body>s</body>"}},
		{"created task", Discard{Kind: OpCreateTask, Payload: map[string]any{"name": "新規", "notes": "平文", "html_notes": "<body>n</body>"}},
			[]DiscardDetail{{"名前", "新規"}, {"説明（Markdown）", "md(<body>n</body>)"}}, []string{"<body>n</body>"}},
		{"updated task", Discard{Kind: OpUpdateTask, Payload: map[string]any{"notes": "平文", "html_notes": "<body>u</body>"}},
			[]DiscardDetail{{"説明（Markdown）", "md(<body>u</body>)"}}, []string{"<body>u</body>"}},
		{"blank Markdown falls back to the plain text", Discard{Kind: OpAddComment, Payload: map[string]any{"text": "平文", "html_text": "<body>blank</body>"}},
			[]DiscardDetail{{"コメント", "平文"}}, []string{"<body>blank</body>"}},
		{"empty Markdown and no plain text", Discard{Kind: OpUpdateTask, Payload: map[string]any{"html_notes": "<body>empty</body>"}},
			nil, []string{"<body>empty</body>"}},
		{"no rich text: not called", Discard{Kind: OpAddComment, Payload: map[string]any{"text": "平文"}},
			[]DiscardDetail{{"コメント", "平文"}}, nil},
		{"rich text of another kind: not called", Discard{Kind: OpAddTag, Payload: map[string]any{"html_text": "<body>x</body>"}},
			nil, nil},
		{"attachment: not called", Discard{Kind: OpUploadAttachment, Payload: map[string]any{"path": "/cache/a.pdf", "html_text": "<body>x</body>"}},
			[]DiscardDetail{{"ファイルのコピー", "/cache/a.pdf"}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen = nil
			if got := DiscardDetails(tc.d, toMarkdown); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(seen, tc.seen) {
				t.Errorf("converted %q, want %q", seen, tc.seen)
			}
		})
	}
}
