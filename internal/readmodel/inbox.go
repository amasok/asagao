package readmodel

import "strings"

// Inbox availability (InboxState.Status), as sync records it.
const (
	InboxOK = "ok"
	// InboxForbidden: the login lacks the notifications:read scope (Asana answered 401/403).
	InboxForbidden = "forbidden"
	// InboxPayment: not available on the workspace's plan (402).
	InboxPayment = "payment"
	// InboxError: another error Asana keeps answering (4xx).
	InboxError = "error"
)

// InboxState says whether the notifications can be shown. When they cannot, front ends
// show the recently modified tasks instead.
type InboxState struct {
	Status string `json:"status"`          // InboxOK, InboxForbidden, InboxPayment, InboxError or ""
	Error  string `json:"error,omitempty"` // Asana's message
	// WriteForbidden: notifications can be listed but not marked read / unread.
	WriteForbidden bool `json:"write_forbidden,omitempty"`
}

// Available reports whether notifications were fetched (at the latest sync).
func (s InboxState) Available() bool { return s.Status == InboxOK }

// Notification is a cached inbox item.
type Notification struct {
	GID          string `json:"gid"`
	WorkspaceGID string `json:"workspace_gid"`
	Read         bool   `json:"read"`
	CreatedAt    string `json:"created_at"`
	ActorGID     string `json:"actor_gid,omitempty"`
	ActorName    string `json:"actor_name,omitempty"`
	// Subtype is the story's resource_subtype (comment_added, assigned, ...).
	Subtype string `json:"subtype,omitempty"`
	Text    string `json:"text,omitempty"`
	// Target is what the notification is about: usually a task (TargetType "task").
	TargetGID  string `json:"target_gid,omitempty"`
	TargetType string `json:"target_type,omitempty"`
	TargetName string `json:"target_name,omitempty"`
	// TaskCached: the target task is in the local store (it can be shown offline).
	TaskCached bool `json:"task_cached"`
	// Pending: a read / unread change has not been sent to Asana yet.
	Pending bool `json:"pending"`
}

// IsTask reports whether the notification is about a task.
func (n Notification) IsTask() bool { return n.TargetType == "task" && n.TargetGID != "" }

// NotificationFilter selects cached notifications.
type NotificationFilter struct {
	UnreadOnly bool
	Limit      int
}

// NotificationLabel says in a few words what kind of activity a notification reports.
func NotificationLabel(subtype string) string {
	switch subtype {
	case "comment_added":
		return "コメント"
	case "assigned":
		return "担当に設定"
	case "unassigned":
		return "担当から外す"
	case "marked_complete":
		return "完了"
	case "marked_incomplete":
		return "未完了に戻す"
	case "due_date_changed", "due_today", "due_soon", "due_date_approaching":
		return "期日"
	case "liked", "comment_liked":
		return "いいね"
	case "collaborator_added", "follower_added":
		return "フォロワーに追加"
	case "added_to_project":
		return "プロジェクトに追加"
	case "mentioned", "mention":
		return "メンション"
	case "attachment_added":
		return "添付"
	case "":
		return ""
	}
	return strings.ReplaceAll(subtype, "_", " ")
}
