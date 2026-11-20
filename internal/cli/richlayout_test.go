package cli

import (
	"regexp"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/store"
)

// layoutTag matches the tags Asana accepts only in tasks and project briefs: headings,
// rules, images and tables (https://developers.asana.com/docs/rich-text).
var layoutTag = regexp.MustCompile(`(?i)<(h[1-6]|hr|img|table|tr|td|object)\b`)

// A comment and a status update written with a heading, a rule and an image are queued
// without the tags Asana rejects there (#63), keeping what was written; a task's notes
// keep them.
func TestCLIRichTextTagsPerTarget(t *testing.T) {
	md := "# 予定\n\n---\n\n![図](asana:42)"
	for _, tc := range []struct {
		name   string
		args   []string
		kind   store.Kind
		field  string
		layout bool // headings, rules and images are elements
	}{
		{"comment", []string{"comment", "add", "t1", md}, store.OpAddComment, "html_text", false},
		{"status", []string{"project", "status", "post", "p1", "--text", md}, store.OpCreateStatus, "html_text", false},
		{"task", []string{"task", "create", "メモ", "--notes", md}, store.OpCreateTask, "html_notes", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeCLIEnv(t)
			if out, err := runCLI(t, tc.args...); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			ops := cliOps(t, path)
			if len(ops) != 1 || ops[0].Kind != tc.kind {
				t.Fatalf("ops = %v, want one %s", opKinds(ops), tc.kind)
			}
			h, _ := ops[0].Payload[tc.field].(string)
			if tc.layout {
				for _, want := range []string{"<h1>予定</h1>", "<hr/>", `<img data-asana-gid="42"/>`} {
					if !strings.Contains(h, want) {
						t.Errorf("%s should keep %s: %q", tc.field, want, h)
					}
				}
				return
			}
			if m := layoutTag.FindString(h); m != "" {
				t.Errorf("%s queued with %s>, which Asana rejects there: %q", tc.field, m, h)
			}
			for _, want := range []string{"<strong>予定</strong>", "---", "![図](asana:42)"} {
				if !strings.Contains(h, want) {
					t.Errorf("%s lost %q: %q", tc.field, want, h)
				}
			}
		})
	}
}
