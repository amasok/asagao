// Package commitref reads the Asana task references in a git commit message (#8).
//
// A reference is one of:
//
//   - "#<gid>" where gid has at least MinBareDigits digits. GitHub issue and pull request
//     numbers ("#123") are short; Asana task gids are long (13 to 16 digits and more), so a
//     short number after "#" is left alone.
//   - "asana#<gid>" (the prefix in any case) with any number of digits, to name a task
//     whose gid is shorter, or to make the reference explicit.
//
// The "#" (or "asana") must not follow a letter, a digit, "_", "&", "/" or "#", so that
// "abc#1234567890", "&#1234567890;" and "owner/repo#1234567890" are not references, and
// the digits must not run into a letter, a digit or "_".
//
// A reference right after one of the GitHub closing keywords (fix, fixes, fixed, close,
// closes, closed, resolve, resolves, resolved; any case, then spaces or a colon on the same
// line) marks the task to be completed: "fixes #1204567890123456". As on GitHub, a keyword
// applies to the one reference after it ("fixes #A, #B" completes A and only mentions B).
// In a revert ("Revert \"...\"" as git writes it), references only mention the task:
// reverting a fix does not complete it again.
package commitref

import (
	"regexp"
	"strings"
)

// MinBareDigits is the number of digits a "#<gid>" reference needs without the "asana"
// prefix.
const MinBareDigits = 10

// maxDigits bounds a gid: longer runs of digits are not gids.
const maxDigits = 32

// Ref is a task referenced by a commit message.
type Ref struct {
	// TaskGID is the Asana gid of the task.
	TaskGID string
	// Fixes is set when a closing keyword comes before a reference to the task.
	Fixes bool
}

// refRE matches a reference with what comes before it: the start or a character that may
// come before a reference (group 1), a closing keyword (group 2), the "asana" prefix
// (group 3) and the gid (group 4). The character after the gid is checked by Parse (RE2
// has no lookahead).
var refRE = regexp.MustCompile(`(?i)(^|[^0-9a-z_&/#])(?:(fix|fixes|fixed|close|closes|closed|resolve|resolves|resolved)(?:[ \t]*:[ \t]*|[ \t]+))?(asana)?#([0-9]+)`)

// Parse returns the tasks that msg references, in the order they first appear, each once.
// A task referenced several times is to be completed when any of its references is.
func Parse(msg string) []Ref {
	revert := strings.HasPrefix(msg, `Revert "`)
	var refs []Ref
	index := map[string]int{}
	for _, m := range refRE.FindAllStringSubmatchIndex(msg, -1) {
		gid := msg[m[8]:m[9]]
		if m[9] < len(msg) && isWordByte(msg[m[9]]) {
			continue // "#1234567890abc": not a gid
		}
		prefixed := m[6] >= 0
		if len(gid) > maxDigits || (!prefixed && len(gid) < MinBareDigits) {
			continue
		}
		fixes := m[4] >= 0 && !revert
		if i, ok := index[gid]; ok {
			refs[i].Fixes = refs[i].Fixes || fixes
			continue
		}
		index[gid] = len(refs)
		refs = append(refs, Ref{TaskGID: gid, Fixes: fixes})
	}
	return refs
}

func isWordByte(b byte) bool {
	return b == '_' || '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

// Marker is the line a comment made for commit sha ends with. Finding it in a task's
// comments means the commit was handled for that task already, so handling it again (a
// second scan, the hook and a scan of the same range) adds nothing.
func Marker(sha string) string { return "asagao-git: " + sha }

// Amended returns the refs of an amended commit that its previous version (whose refs are
// prev) did not have already: the previous version was handled when it was made, so a ref
// it had is left out unless the amend adds a closing keyword to it.
func Amended(prev, refs []Ref) []Ref {
	had := map[string]bool{} // gid -> fixes
	for _, r := range prev {
		had[r.TaskGID] = had[r.TaskGID] || r.Fixes
	}
	var out []Ref
	for _, r := range refs {
		if fixes, ok := had[r.TaskGID]; ok && (fixes || !r.Fixes) {
			continue
		}
		out = append(out, r)
	}
	return out
}
