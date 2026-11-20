package tui

import (
	"context"
	"fmt"
	"github.com/amasok/asagao/internal/app"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// The inbox scope: the user's Asana notifications (cached by sync), newest first and
// grouped by day. A notification about a task selects that task (detail pane, actions);
// enter opens it and marks the notification read. When the notifications cannot be
// fetched, the scope is "最近の更新" (tasks modified in the last 7 days) with a note why.

const inboxName = "受信トレイ"

// inboxScope returns the second entry of the scope list: the inbox, or recent updates.
func (m *Model) inboxScope() scope {
	if m.inbox.Available() {
		name := inboxName
		if m.inboxUnread > 0 {
			name = fmt.Sprintf("%s (%d)", inboxName, m.inboxUnread)
		}
		sc := scope{kind: scopeInbox, name: name}
		if m.inbox.WriteForbidden {
			sc.notice = sync.InboxReadOnlyMessage(m.deps.AuthScopes)
		}
		return sc
	}
	sc := scope{kind: scopeRecent, name: "最近の更新"}
	if m.inbox.Status != "" { // tried and failed (not merely "not synced yet")
		sc.notice = sync.InboxUnavailableMessage(m.inbox, m.deps.AuthScopes)
	}
	return sc
}

// inboxRows loads the notifications as list rows. Rows about a cached task carry it (the
// detail pane shows it, task actions apply to it); enter on one about a task not cached
// yet fetches it. notice (if any) is shown above the list.
func inboxRows(ctx context.Context, st *app.UseCases, now time.Time, notice string) ([]row, error) {
	ns, err := st.Query().ListNotifications(ctx, readmodel.NotificationFilter{})
	if err != nil {
		return nil, err
	}
	tasks, err := st.Query().TaskSummaries(ctx, notificationTaskGIDs(ns))
	if err != nil {
		return nil, err
	}
	var rows []row
	if notice != "" {
		rows = append(rows, row{header: notice, note: true})
	}
	var label string
	for i := range ns {
		n := &ns[i]
		if l := modifiedDay(n.CreatedAt, now); l != label || i == 0 {
			label = l
			rows = append(rows, row{header: l, key: "inbox:" + l})
		}
		r := row{notif: n}
		if t, ok := tasks[n.TargetGID]; ok && n.IsTask() {
			r.task = &t
		}
		rows = append(rows, r)
	}
	// header counts
	for i := range rows {
		if rows[i].isHeader() && !rows[i].note {
			for j := i + 1; j < len(rows) && !rows[j].isHeader(); j++ {
				rows[i].count++
			}
		}
	}
	return rows, nil
}

func notificationTaskGIDs(ns []readmodel.Notification) []string {
	var gids []string
	for _, n := range ns {
		if n.IsTask() {
			gids = append(gids, n.TargetGID)
		}
	}
	return gids
}

// selectedNotification returns the notification under the list cursor (inbox only).
func (m *Model) selectedNotification() *readmodel.Notification {
	if m.currentScope().kind != scopeInbox || m.list.cursor < 0 || m.list.cursor >= len(m.list.rows) {
		return nil
	}
	return m.list.rows[m.list.cursor].notif
}

// openNotification opens what the selected notification is about and marks it read.
func (m *Model) openNotification() tea.Cmd {
	n := m.selectedNotification()
	if n == nil {
		return nil
	}
	var cmds []tea.Cmd
	if !n.Read && !m.inbox.WriteForbidden { // (when Asana refuses, the note above the list says why)
		cmds = append(cmds, m.markNotifications([]string{n.GID}, true, ""))
	}
	switch {
	case m.selectedTask() != nil:
		cmds = append(cmds, m.openDetail())
	case n.IsTask(): // not cached yet: fetched as it is opened (offline: says so)
		cmds = append(cmds, m.openTaskDetail(n.TargetGID))
	case n.TargetType == "project":
		for i, s := range m.scopes {
			if s.kind == scopeProject && s.project == n.TargetGID {
				cmds = append(cmds, m.moveScope(i-m.scopeCursor))
				return tea.Batch(cmds...)
			}
		}
		m.status = "プロジェクト「" + n.TargetName + "」は同期していません"
	default:
		m.status = "タスク以外の通知です（" + n.TargetName + "）。Asana で確認してください"
	}
	return tea.Batch(cmds...)
}

// toggleNotificationRead marks the selected notification read, or unread if it is read.
func (m *Model) toggleNotificationRead() tea.Cmd {
	n := m.selectedNotification()
	if n == nil {
		m.status = "通知を選んでください"
		return nil
	}
	status := "既読にしました"
	if n.Read {
		status = "未読に戻しました"
	}
	return m.markNotifications([]string{n.GID}, !n.Read, status)
}

// markAllNotificationsRead marks every unread notification read.
func (m *Model) markAllNotificationsRead() tea.Cmd {
	var gids []string
	for _, r := range m.list.all {
		if r.notif != nil && !r.notif.Read {
			gids = append(gids, r.notif.GID)
		}
	}
	if len(gids) == 0 {
		m.status = "未読の通知はありません"
		return nil
	}
	return m.markNotifications(gids, true, fmt.Sprintf("%d 件を既読にしました", len(gids)))
}

func (m *Model) markNotifications(gids []string, read bool, status string) tea.Cmd {
	notifications := m.notifications(m.deps.Now())
	return func() tea.Msg {
		if _, err := notifications.MarkHere(m.ctx, gids, read); err != nil {
			return writeDoneMsg{err: err}
		}
		return writeDoneMsg{status: status}
	}
}

// notificationLine draws one notification: unread mark, who, what, the task, and when.
func (m *Model) notificationLine(n *readmodel.Notification, w int, selected bool) string {
	dot, sent := " ", " "
	if !n.Read {
		dot = "●"
	}
	if n.Pending { // the read / unread change is not sent yet
		sent = "↑"
	}
	mark := dot + sent
	when := ""
	if t, err := time.Parse(time.RFC3339, n.CreatedAt); err == nil {
		when = t.In(m.deps.Now().Location()).Format("15:04")
	}
	what := readmodel.NotificationLabel(n.Subtype)
	who := n.ActorName
	if what != "" {
		who += " " + what
	}
	if who != "" {
		who += ": "
	}
	body := n.TargetName
	if n.Text != "" && n.Subtype == "comment_added" {
		body += " — " + n.Text
	}
	timeW := 5
	nameW := max(4, w-ansi.StringWidth(mark)-1-timeW)
	text := ansi.Truncate(who+body, nameW, "…")
	if selected {
		return m.highlight(mark+padRight(text, nameW)+" "+padLeft(when, timeW), w, true, m.focus == paneList)
	}
	style := lipgloss.NewStyle()
	if n.Read {
		style = m.st.dim
	} else {
		mark = m.st.header.Render(mark)
	}
	return mark + style.Render(padRight(text, nameW)) + " " + m.st.dim.Render(padLeft(when, timeW))
}
