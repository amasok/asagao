package readmodel

import (
	"encoding/json"
	"testing"
)

func TestInboxStateAvailable(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{
		{InboxOK, true},
		{"ok", true},
		{"", false},
		{InboxForbidden, false},
		{InboxPayment, false},
		{InboxError, false},
		{"OK", false},
	} {
		if got := (InboxState{Status: tc.status}).Available(); got != tc.want {
			t.Errorf("InboxState{Status: %q}.Available() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

// The values sync records under the inbox status key: changing them would misread what an
// older version recorded.
func TestInboxStatusValues(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{InboxOK, "ok"}, {InboxForbidden, "forbidden"}, {InboxPayment, "payment"}, {InboxError, "error"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}

func TestNotificationIsTask(t *testing.T) {
	for _, tc := range []struct {
		typ, gid string
		want     bool
	}{
		{"task", "t1", true},
		{"task", "", false},
		{"project", "p1", false},
		{"", "t1", false},
		{"Task", "t1", false},
	} {
		if got := (Notification{TargetType: tc.typ, TargetGID: tc.gid}).IsTask(); got != tc.want {
			t.Errorf("Notification{TargetType: %q, TargetGID: %q}.IsTask() = %v, want %v", tc.typ, tc.gid, got, tc.want)
		}
	}
}

func TestNotificationLabel(t *testing.T) {
	for _, tc := range []struct{ subtype, want string }{
		{"comment_added", "コメント"},
		{"assigned", "担当に設定"},
		{"unassigned", "担当から外す"},
		{"marked_complete", "完了"},
		{"marked_incomplete", "未完了に戻す"},
		{"due_date_changed", "期日"},
		{"due_today", "期日"},
		{"due_soon", "期日"},
		{"due_date_approaching", "期日"},
		{"liked", "いいね"},
		{"comment_liked", "いいね"},
		{"collaborator_added", "フォロワーに追加"},
		{"follower_added", "フォロワーに追加"},
		{"added_to_project", "プロジェクトに追加"},
		{"mentioned", "メンション"},
		{"mention", "メンション"},
		{"attachment_added", "添付"},
		{"", ""},
		// anything else: its words
		{"section_changed", "section changed"},
		{"name_changed_twice", "name changed twice"},
		{"x", "x"},
	} {
		if got := NotificationLabel(tc.subtype); got != tc.want {
			t.Errorf("NotificationLabel(%q) = %q, want %q", tc.subtype, got, tc.want)
		}
	}
}

// The JSON the CLI and the MCP tools return for the inbox.
func TestInboxJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"state empty", InboxState{}, `{"status":""}`},
		{"state", InboxState{Status: InboxOK, Error: "e", WriteForbidden: true}, `{"status":"ok","error":"e","write_forbidden":true}`},
		{"notification empty", Notification{}, `{"gid":"","workspace_gid":"","read":false,"created_at":"","task_cached":false,"pending":false}`},
		{"notification", Notification{GID: "n1", WorkspaceGID: "w1", Read: true, CreatedAt: "2026-01-02T00:00:00Z", ActorGID: "u1", ActorName: "花子",
			Subtype: "comment_added", Text: "了解", TargetGID: "t1", TargetType: "task", TargetName: "牛乳", TaskCached: true, Pending: true},
			`{"gid":"n1","workspace_gid":"w1","read":true,"created_at":"2026-01-02T00:00:00Z","actor_gid":"u1","actor_name":"花子","subtype":"comment_added","text":"了解","target_gid":"t1","target_type":"task","target_name":"牛乳","task_cached":true,"pending":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(tc.v)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}
