package tui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/termsafe"
)

// quickCreate creates a task from what the user typed in the create prompt (a and S):
// the name with the quick-add tokens at its end, "牛乳を買う @hanako 明日 #買い物" (see
// app.ParseQuickAdd). in is where the task goes; its name, due date, assignee and tags
// come from line. defaultDue is the date used when line has none (the calendar's day,
// or ""). now is the time the key was pressed: relative dates and the write use it.
//
// The status line says what was read, and which token was not read and why.
// done is the message before the task's name ("タスクを作成しました: "); with keep the
// selection stays where it is (the new task is not selected).
func (m *Model) quickCreate(line string, now time.Time, in app.CreateHereInput, defaultDue, done string, keep bool) tea.Cmd {
	ctx, st, tasks := m.ctx, m.deps.Store, m.tasks(now)
	return func() tea.Msg {
		q, err := app.ReadQuickAdd(ctx, st, line, now)
		if err != nil {
			return writeDoneMsg{err: err}
		}
		in.Name, in.Due, in.Tags = q.Name, q.Due, q.Tags
		if in.Due == "" {
			in.Due = defaultDue
		}
		if q.Assignee != nil {
			gid := q.Assignee.GID
			in.Assignee = &gid
		}
		gid, err := tasks.CreateHere(ctx, in)
		if err != nil && gid == "" {
			return writeDoneMsg{err: err}
		}
		status := done + q.Name + q.Summary()
		if err != nil { // created, but the tags could not be added
			status += " エラー: タグを付けられませんでした: " + termsafe.String(err.Error())
		}
		if keep {
			gid = ""
		}
		return writeDoneMsg{status: status, gid: gid}
	}
}
