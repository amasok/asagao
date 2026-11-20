package store

import (
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/ui/format"
)

// Human-readable (Japanese) descriptions of outbox entries, shared by the TUI and the CLI,
// are format's (ui/format). These call them, so that existing callers stay working.

// OpStateLabel is format.OpStateLabel.
func OpStateLabel(state string) string { return format.OpStateLabel(state) }

// VerifyNote is format.VerifyNote.
const VerifyNote = format.VerifyNote

// OpLabel is format.OpLabel.
func OpLabel(op Op) string { return format.OpLabel(op) }

// OpNote is format.OpNote.
func OpNote(op Op) (heading, text string) { return format.OpNote(op) }

// DiscardNotice is format.DiscardNotice.
const DiscardNotice = format.DiscardNotice

// DiscardLine is format.DiscardLine.
func DiscardLine(d Discard) string { return format.DiscardLine(d) }

// IsResetDiscard is format.IsResetDiscard.
func IsResetDiscard(d Discard) bool { return format.IsResetDiscard(d) }

// DiscardDetail is format.DiscardDetail as a type of the store's own, not an alias: this
// package's tests write it without field names, which go vet allows only for a type of the
// same package.
type DiscardDetail format.DiscardDetail

// DiscardDetails is format.DiscardDetails.
func DiscardDetails(d Discard, toMarkdown func(html string) string) []DiscardDetail {
	var out []DiscardDetail
	for _, dt := range format.DiscardDetails(d, toMarkdown) {
		out = append(out, DiscardDetail(dt))
	}
	return out
}

// AccountChangedHelp is format.AccountChangedHelp.
func AccountChangedHelp(unsent int, resetHow string) string {
	return format.AccountChangedHelp(unsent, resetHow)
}

// FieldLabel is format.FieldLabel.
func FieldLabel(k string) string { return format.FieldLabel(k) }

// ConflictLines is format.ConflictLines.
func ConflictLines(op Op) []string { return format.ConflictLines(op) }

// DescribeOp is format.DescribeOp.
func DescribeOp(op Op) string { return format.DescribeOp(op) }

// DescribeChange is format.DescribeChange.
func DescribeChange(op Op) string { return format.DescribeChange(op) }

// SubtypeLabel is task.SubtypeLabel (kept here until the entry points stop using store).
func SubtypeLabel(s string) string { return task.SubtypeLabel(s) }

// ApprovalLabel is task.ApprovalLabel.
func ApprovalLabel(s string) string { return task.ApprovalLabel(s) }

// ParseSubtype is task.ParseSubtype.
func ParseSubtype(s string) (string, error) { return task.ParseSubtype(s) }

// ParseApprovalStatus is task.ParseApprovalStatus.
func ParseApprovalStatus(s string) (string, error) { return task.ParseApprovalStatus(s) }

// TaskBadges is format.TaskBadges.
func TaskBadges(t TaskSummary) string { return format.TaskBadges(t) }
