package task

import (
	"strings"
	"testing"
)

func TestValidateFields(t *testing.T) {
	for _, tc := range []struct {
		name    string
		changes map[string]any
		wantErr string // "" for none
	}{
		{"nothing", map[string]any{}, "変更がありません"},
		{"nil", nil, "変更がありません"},
		{"name", map[string]any{"name": "買い物"}, ""},
		{"every field", map[string]any{"completed": true, "name": "x", "notes": "", "html_notes": "<body></body>", "due_on": "", "assignee": "", "liked": false,
			"resource_subtype": "approval", "approval_status": "approved"}, ""},
		{"unknown field", map[string]any{"start_on": "2026-11-20"}, `unsupported field "start_on"`},
		{"completed as string", map[string]any{"completed": "true"}, "completed must be bool"},
		{"liked as string", map[string]any{"liked": "yes"}, "liked must be bool"},
		{"name as bool", map[string]any{"name": true}, "name must be string"},
		{"due_on as nil", map[string]any{"due_on": nil}, "due_on must be string"},
		{"assignee as number", map[string]any{"assignee": 1200}, "assignee must be string"},
		{"blank name", map[string]any{"name": " \t\n"}, "タスク名が空です"},
		{"empty name", map[string]any{"name": ""}, "タスク名が空です"},
		{"every subtype", map[string]any{"resource_subtype": "milestone"}, ""},
		{"default subtype", map[string]any{"resource_subtype": "default_task"}, ""},
		{"unknown subtype", map[string]any{"resource_subtype": "section"}, `タスクの種類は default_task / milestone / approval / custom のどれかです: "section"`},
		{"label is not a subtype", map[string]any{"resource_subtype": "承認"}, "タスクの種類は"},
		{"every approval status", map[string]any{"approval_status": "changes_requested"}, ""},
		{"unknown approval status", map[string]any{"approval_status": "done"}, `承認の状態は pending / approved / rejected / changes_requested のどれかです: "done"`},
		{"empty approval status", map[string]any{"approval_status": ""}, "承認の状態は"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFields(tc.changes)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateFields = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateFields = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestCheckApproval(t *testing.T) {
	for _, tc := range []struct {
		subtype string
		changes map[string]any
		ok      bool
	}{
		{"approval", map[string]any{"approval_status": "approved"}, true},
		{"default_task", map[string]any{"approval_status": "approved"}, false},
		{"milestone", map[string]any{"approval_status": "rejected"}, false},
		{"", map[string]any{"approval_status": "pending"}, false},
		{"default_task", map[string]any{"approval_status": "approved", "resource_subtype": "approval"}, true},  // made an approval in the same edit
		{"approval", map[string]any{"approval_status": "approved", "resource_subtype": "default_task"}, false}, // no longer one
		{"default_task", map[string]any{"name": "x"}, true},                                                    // no approval status
		{"default_task", map[string]any{"resource_subtype": "milestone"}, true},
		{"default_task", map[string]any{}, true},
	} {
		err := CheckApproval(tc.subtype, tc.changes)
		if tc.ok != (err == nil) {
			t.Errorf("CheckApproval(%q, %v) = %v, want ok = %v", tc.subtype, tc.changes, err, tc.ok)
		}
		if err != nil && !strings.Contains(err.Error(), "承認タスクではないため") {
			t.Errorf("CheckApproval(%q, %v) = %v", tc.subtype, tc.changes, err)
		}
	}
}
