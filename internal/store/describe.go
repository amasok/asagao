package store

import (
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
)

// Human-readable (Japanese) descriptions of outbox entries, shared by the TUI and the CLI,
// are readmodel's (readmodel/describe.go). These call them, so that the entry points do not
// change until they stop using the store.

// OpStateLabel is readmodel.OpStateLabel.
func OpStateLabel(state string) string { return readmodel.OpStateLabel(state) }

// VerifyNote is readmodel.VerifyNote.
const VerifyNote = readmodel.VerifyNote

// OpLabel is readmodel.OpLabel.
func OpLabel(op Op) string { return readmodel.OpLabel(op) }

// OpNote is readmodel.OpNote.
func OpNote(op Op) (heading, text string) { return readmodel.OpNote(op) }

// DiscardNotice is readmodel.DiscardNotice.
const DiscardNotice = readmodel.DiscardNotice

// DiscardLine is readmodel.DiscardLine.
func DiscardLine(d Discard) string { return readmodel.DiscardLine(d) }

// IsResetDiscard is readmodel.IsResetDiscard.
func IsResetDiscard(d Discard) bool { return readmodel.IsResetDiscard(d) }

// DiscardDetail is readmodel.DiscardDetail as a type of the store's own, not an alias: this
// package's tests write it without field names, which go vet allows only for a type of the
// same package.
type DiscardDetail readmodel.DiscardDetail

// DiscardDetails is readmodel.DiscardDetails.
func DiscardDetails(d Discard, toMarkdown func(html string) string) []DiscardDetail {
	var out []DiscardDetail
	for _, dt := range readmodel.DiscardDetails(d, toMarkdown) {
		out = append(out, DiscardDetail(dt))
	}
	return out
}

// AccountChangedHelp is readmodel.AccountChangedHelp.
func AccountChangedHelp(unsent int, resetHow string) string {
	return readmodel.AccountChangedHelp(unsent, resetHow)
}

// FieldLabel is readmodel.FieldLabel.
func FieldLabel(k string) string { return readmodel.FieldLabel(k) }

// ConflictLines is readmodel.ConflictLines.
func ConflictLines(op Op) []string { return readmodel.ConflictLines(op) }

// DescribeOp is readmodel.DescribeOp.
func DescribeOp(op Op) string { return readmodel.DescribeOp(op) }

// DescribeChange is readmodel.DescribeChange.
func DescribeChange(op Op) string { return readmodel.DescribeChange(op) }

// SubtypeLabel is task.SubtypeLabel (kept here until the entry points stop using store).
func SubtypeLabel(s string) string { return task.SubtypeLabel(s) }

// ApprovalLabel is task.ApprovalLabel.
func ApprovalLabel(s string) string { return task.ApprovalLabel(s) }

// ParseSubtype is task.ParseSubtype.
func ParseSubtype(s string) (string, error) { return task.ParseSubtype(s) }

// ParseApprovalStatus is task.ParseApprovalStatus.
func ParseApprovalStatus(s string) (string, error) { return task.ParseApprovalStatus(s) }

// TaskBadges is readmodel.TaskBadges.
func TaskBadges(t TaskSummary) string { return readmodel.TaskBadges(t) }
