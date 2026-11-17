package task

import (
	"strings"
	"testing"
)

func TestParseSubtypeAndApprovalStatus(t *testing.T) {
	for _, tc := range []struct {
		parse    func(string) (string, error)
		in, want string
	}{
		{ParseSubtype, "milestone", "milestone"},
		{ParseSubtype, " Milestone ", "milestone"},
		{ParseSubtype, "マイルストーン", "milestone"},
		{ParseSubtype, "承認", "approval"},
		{ParseSubtype, "task", "default_task"},
		{ParseSubtype, "Default", "default_task"},
		{ParseSubtype, "normal", "default_task"},
		{ParseSubtype, "タスク", "default_task"},
		{ParseApprovalStatus, "approve", "approved"},
		{ParseApprovalStatus, "承認", "approved"},
		{ParseApprovalStatus, "承認済み", "approved"},
		{ParseApprovalStatus, "REJECT", "rejected"},
		{ParseApprovalStatus, "却下", "rejected"},
		{ParseApprovalStatus, "修正依頼", "changes_requested"},
		{ParseApprovalStatus, "要修正", "changes_requested"},
		{ParseApprovalStatus, "pending", "pending"},
	} {
		if got, err := tc.parse(tc.in); err != nil || got != tc.want {
			t.Errorf("parse(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	_, err := ParseSubtype("bug")
	if want := `"bug" は使えません。default_task（タスク） / milestone（マイルストーン） / approval（承認） のどれかを指定してください`; err == nil || err.Error() != want {
		t.Errorf("ParseSubtype(bug) = %v, want %s", err, want)
	}
	if _, err := ParseApprovalStatus("maybe"); err == nil || !strings.Contains(err.Error(), "changes_requested（要修正）") {
		t.Errorf("ParseApprovalStatus(maybe) = %v", err)
	}
}
