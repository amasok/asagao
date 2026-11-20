package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/termsafe"
)

// Acting on several tasks at once: in the task list, tasks are marked (V, or * for all
// shown), and while some are marked the complete / move / assignee / due keys act on the
// marked tasks instead of the selected one, after a "N 件を…します (y/n)" confirmation.
//
// Marks are kept by gid, and only for tasks shown in the list: a reload, a filter or a
// collapsed group that hides a task drops its mark, so nothing unseen is changed. Each task
// is written with the same use case as a single change (one outbox entry per task); one
// that cannot be written does not stop the others.

// bulkPlan is a change to several tasks, waiting for the y/n confirmation.
type bulkPlan struct {
	question string // after "N 件": "を完了にします"
	done     string // "完了にしました" (for the result: "N 件を完了にしました")
	tasks    []readmodel.TaskSummary
	// apply makes the change to one task; now is when y was pressed.
	apply func(m *Model, now time.Time) func(ctx context.Context, t readmodel.TaskSummary) error
}

// bulkUsersMsg: the members, to choose the assignee of the marked tasks.
type bulkUsersMsg struct {
	tasks []readmodel.TaskSummary
	users []readmodel.UserRef
	err   error
}

// markable reports whether row r can be marked: a task in the list (not a notification).
func markable(r row) bool { return r.task != nil && r.notif == nil }

// markedTasks are the marked tasks shown in the list, in list order. Marks only act in
// the list (not on the board, the calendar or the detail view).
func (m *Model) markedTasks() []readmodel.TaskSummary {
	if len(m.list.marked) == 0 || m.keyContext() != ctxList {
		return nil
	}
	var out []readmodel.TaskSummary
	for _, r := range m.list.rows {
		if markable(r) && m.list.marked[r.task.GID] {
			out = append(out, *r.task)
		}
	}
	return out
}

// pruneMarks drops the marks of tasks no longer shown in the list, and narrows a bulk
// change that is being chosen or confirmed to the tasks still shown (as they are now):
// what was marked when the key was pressed may have been hidden by a reload since.
func (m *Model) pruneMarks() {
	if len(m.list.marked) == 0 && m.bulkAsk == nil && m.prompt.bulkDue == nil && (m.picker == nil || m.picker.bulk == nil) {
		return
	}
	shown := m.shownTasks()
	for gid := range m.list.marked {
		if _, ok := shown[gid]; !ok {
			delete(m.list.marked, gid)
		}
	}
	const gone = "対象のタスクが一覧から見えなくなったので取り消しました"
	if p := m.bulkAsk; p != nil {
		if p.tasks = stillShown(p.tasks, shown); len(p.tasks) == 0 {
			m.bulkAsk, m.status = nil, gone
		}
	}
	if p := m.picker; p != nil && p.bulk != nil {
		n := len(p.bulk)
		if p.bulk = stillShown(p.bulk, shown); len(p.bulk) == 0 {
			m.picker, m.status = nil, gone
		} else {
			p.title = strings.Replace(p.title, fmt.Sprintf("%d 件", n), fmt.Sprintf("%d 件", len(p.bulk)), 1)
		}
	}
	if m.prompt.bulkDue != nil {
		if m.prompt.bulkDue = stillShown(m.prompt.bulkDue, shown); len(m.prompt.bulkDue) == 0 {
			m.prompt.bulkDue, m.status = nil, gone
			if m.prompt.mode == promptDue {
				m.prompt.mode = promptNone
				m.prompt.input.Blur()
			}
		}
	}
}

// shownTasks are the markable tasks in the list, by gid.
func (m *Model) shownTasks() map[string]readmodel.TaskSummary {
	shown := map[string]readmodel.TaskSummary{}
	for _, r := range m.list.rows {
		if markable(r) {
			shown[r.task.GID] = *r.task
		}
	}
	return shown
}

// stillShown keeps the tasks that are shown, as they are shown now; never nil.
func stillShown(tasks []readmodel.TaskSummary, shown map[string]readmodel.TaskSummary) []readmodel.TaskSummary {
	out := []readmodel.TaskSummary{}
	for _, t := range tasks {
		if now, ok := shown[t.GID]; ok {
			out = append(out, now)
		}
	}
	return out
}

// showRows recomputes the shown rows (after a reload or a collapse) and drops the marks of
// tasks that are no longer shown.
func (m *Model) showRows() {
	m.list.rows = m.visibleRows()
	m.pruneMarks()
}

// toggleMark marks (or unmarks) the selected task and moves to the next one.
func (m *Model) toggleMark() tea.Cmd {
	if m.list.cursor < 0 || m.list.cursor >= len(m.list.rows) || !markable(m.list.rows[m.list.cursor]) {
		if m.currentScope().kind == scopeInbox {
			m.status = "通知には印を付けられません"
		}
		return nil
	}
	gid := m.list.rows[m.list.cursor].task.GID
	if m.list.marked == nil {
		m.list.marked = map[string]bool{}
	}
	if m.list.marked[gid] {
		delete(m.list.marked, gid)
	} else {
		m.list.marked[gid] = true
	}
	return m.moveList(1)
}

// toggleMarkAll marks every task shown, or unmarks them all when all are marked.
func (m *Model) toggleMarkAll() tea.Cmd {
	var gids []string
	all := true
	for _, r := range m.list.rows {
		if markable(r) {
			gids = append(gids, r.task.GID)
			all = all && m.list.marked[r.task.GID]
		}
	}
	if len(gids) == 0 {
		return nil
	}
	if all {
		m.list.marked = nil
		m.status = "印をすべて外しました"
		return nil
	}
	if m.list.marked == nil {
		m.list.marked = map[string]bool{}
	}
	for _, gid := range gids {
		m.list.marked[gid] = true
	}
	m.status = fmt.Sprintf("%d 件に印を付けました", len(gids))
	return nil
}

// clearMarks unmarks everything (esc in the list); false if nothing was marked.
func (m *Model) clearMarks() bool {
	if len(m.markedTasks()) == 0 {
		return false
	}
	m.list.marked = nil
	m.status = "印をすべて外しました"
	return true
}

// orMarked runs the bulk version of an action when tasks are marked, else the single one.
func orMarked(bulk func(*Model, []readmodel.TaskSummary) tea.Cmd, single func(*Model) tea.Cmd) func(*Model) tea.Cmd {
	return func(m *Model) tea.Cmd {
		if tasks := m.markedTasks(); len(tasks) > 0 {
			return bulk(m, tasks)
		}
		return single(m)
	}
}

// ask shows the confirmation for plan (nothing to do: says so instead).
func (m *Model) ask(plan *bulkPlan, unchanged string) tea.Cmd {
	if len(plan.tasks) == 0 {
		m.status = unchanged
		return nil
	}
	m.bulkAsk = plan
	return nil
}

func (m *Model) handleBulkKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y", "Y":
		plan := m.bulkAsk
		m.bulkAsk, m.list.marked = nil, nil
		return m, m.runBulk(plan)
	case "n", "N", "esc", "q", "ctrl+g":
		m.bulkAsk = nil
		m.status = "取り消しました"
	}
	return m, nil // other keys: keep asking
}

// bulkFooter is the confirmation line while one is waiting.
func (m *Model) bulkFooter() (string, bool) {
	if m.bulkAsk == nil {
		return "", false
	}
	return fmt.Sprintf("%d 件%s (y/n)", len(m.bulkAsk.tasks), m.bulkAsk.question), true
}

// runBulk writes the change to each task of plan, one by one; a task that fails does not
// stop the others. The result goes to the status line.
func (m *Model) runBulk(plan *bulkPlan) tea.Cmd {
	apply := plan.apply(m, m.deps.Now())
	ctx := m.ctx
	return func() tea.Msg {
		ok := 0
		var reasons []string
		failed := map[string]int{}
		for _, t := range plan.tasks {
			if err := apply(ctx, t); err != nil {
				r := termsafe.String(err.Error())
				if failed[r] == 0 {
					reasons = append(reasons, r)
				}
				failed[r]++
				continue
			}
			ok++
		}
		return writeDoneMsg{status: bulkResult(ok, plan.done, reasons, failed)}
	}
}

// bulkResult is the status line after a bulk change: "3 件を完了にしました。2 件は変更
// できませんでした（理由 (2 件)）".
func bulkResult(ok int, done string, reasons []string, failed map[string]int) string {
	s := fmt.Sprintf("%d 件を%s", ok, done)
	if len(reasons) == 0 {
		return s
	}
	n := 0
	parts := make([]string, len(reasons))
	for i, r := range reasons {
		n += failed[r]
		parts[i] = fmt.Sprintf("%s (%d 件)", r, failed[r])
	}
	return s + fmt.Sprintf("。%d 件は変更できませんでした: %s", n, strings.Join(parts, " / "))
}

// --- the changes ---

// bulkComplete completes the marked tasks that are open; when all are completed, it marks
// them incomplete again.
func (m *Model) bulkComplete(tasks []readmodel.TaskSummary) tea.Cmd {
	var open []readmodel.TaskSummary
	for _, t := range tasks {
		if !t.Completed {
			open = append(open, t)
		}
	}
	done := len(open) > 0
	plan := &bulkPlan{tasks: open, question: "を完了にします", done: "完了にしました"}
	if !done {
		plan.tasks = tasks
		plan.question = "を未完了に戻します"
		plan.done = "未完了に戻しました"
	}
	plan.apply = updateAll(app.TaskUpdate{Completed: &done})
	return m.ask(plan, "")
}

// updateAll applies u to each task with the task use case (as updateTask does).
func updateAll(u app.TaskUpdate) func(*Model, time.Time) func(context.Context, readmodel.TaskSummary) error {
	return func(m *Model, now time.Time) func(context.Context, readmodel.TaskSummary) error {
		tasks := m.tasks(now)
		return func(ctx context.Context, t readmodel.TaskSummary) error { return tasks.UpdateHere(ctx, t.GID, u) }
	}
}

// bulkMove chooses a section of the open project (or of the one project all the marked
// tasks are in) to move the marked tasks to.
func (m *Model) bulkMove(tasks []readmodel.TaskSummary) tea.Cmd {
	project := ""
	if sc := m.currentScope(); sc.kind == scopeProject {
		project = sc.project
	} else {
		for i, t := range tasks {
			if len(t.Memberships) != 1 || (i > 0 && t.Memberships[0].ProjectGID != project) {
				project = ""
				break
			}
			project = t.Memberships[0].ProjectGID
		}
	}
	if project == "" {
		m.status = "どのプロジェクトのセクションに移動するか決められません（プロジェクトを開いてから選んでください）"
		return nil
	}
	var sections []readmodel.SectionView
	name := ""
	for _, sc := range m.scopes {
		if sc.project == project {
			sections, name = sc.sections, sc.name
		}
	}
	if len(sections) == 0 {
		m.status = "このプロジェクトにはセクションがありません"
		return nil
	}
	p := &picker{kind: pickSection, title: fmt.Sprintf("%d 件の移動先のセクション（%s）", len(tasks), name), project: project, bulk: tasks}
	for _, s := range sections {
		p.items = append(p.items, pickerItem{label: s.Name, value: s.GID})
	}
	m.picker = p
	return nil
}

// bulkAssignee loads the members to choose the assignee of the marked tasks.
func (m *Model) bulkAssignee(tasks []readmodel.TaskSummary) tea.Cmd {
	st, ctx := m.deps.Store, m.ctx
	return func() tea.Msg {
		users, err := queryOf(st).ListUsers(ctx)
		return bulkUsersMsg{tasks: tasks, users: users, err: err}
	}
}

func (m *Model) showBulkAssignee(msg bulkUsersMsg) {
	// the list may have been reloaded while the members were read
	if msg.tasks = stillShown(msg.tasks, m.shownTasks()); len(msg.tasks) == 0 {
		m.status = "対象のタスクが一覧から見えなくなったので取り消しました"
		return
	}
	me, _ := queryOf(m.deps.Store).MeGID(m.ctx)
	p := &picker{kind: pickAssignee, title: fmt.Sprintf("%d 件の担当者", len(msg.tasks)), bulk: msg.tasks}
	for _, u := range msg.users {
		label := u.Name
		if u.GID == me {
			label += "（自分）"
		}
		p.items = append(p.items, pickerItem{label: label, value: u.GID})
	}
	p.items = append(p.items, pickerItem{label: "（未割り当て）"})
	m.picker = p
}

// bulkDue chooses the due date of the marked tasks (the same choices as for one task).
func (m *Model) bulkDue(tasks []readmodel.TaskSummary) tea.Cmd {
	m.picker = &picker{kind: pickDue, title: fmt.Sprintf("%d 件の期日", len(tasks)), items: dueItems(m.deps.Now()), bulk: tasks}
	return nil
}

// bulkPicked is the choice made in a picker opened for the marked tasks.
func (m *Model) bulkPicked(p *picker, item pickerItem) tea.Cmd {
	switch p.kind {
	case pickSection:
		var move []readmodel.TaskSummary
		for _, t := range p.bulk {
			if sectionIn(t, p.project) != item.value {
				move = append(move, t)
			}
		}
		project, section := p.project, item.value
		return m.ask(&bulkPlan{tasks: move, question: "を「" + item.label + "」に移動します",
			done: "「" + item.label + "」に移動しました",
			apply: func(m *Model, now time.Time) func(context.Context, readmodel.TaskSummary) error {
				sections := m.sections(now)
				return func(ctx context.Context, t readmodel.TaskSummary) error {
					return sections.MoveHere(ctx, t.GID, project, section)
				}
			}}, "すべてすでに「"+item.label+"」にあります")
	case pickAssignee:
		label := strings.TrimSuffix(item.label, "（自分）")
		var change []readmodel.TaskSummary
		for _, t := range p.bulk {
			current := ""
			if t.Assignee != nil {
				current = t.Assignee.GID
			}
			if current != item.value {
				change = append(change, t)
			}
		}
		user := item.value
		return m.ask(&bulkPlan{tasks: change, question: "の担当者を " + label + " にします",
			done: "担当者を " + label + " にしました", apply: updateAll(app.TaskUpdate{Assignee: &user})},
			"すべてすでに担当者が "+label+" です")
	case pickDue:
		if item.value == "?" {
			m.openPrompt(promptDue)
			m.prompt.bulkDue = p.bulk
			return m.prompt.input.Focus()
		}
		return m.askDue(p.bulk, item.value)
	}
	return nil
}

// askDue confirms setting the due date of tasks to due ("" = no due date).
func (m *Model) askDue(tasks []readmodel.TaskSummary, due string) tea.Cmd {
	var change []readmodel.TaskSummary
	for _, t := range tasks {
		if t.DueOn != due {
			change = append(change, t)
		}
	}
	label, unchanged := "期日を "+longDue(due, "")+" に", "すべてすでに期日が "+longDue(due, "")+" です"
	if due == "" {
		label, unchanged = "期日をなしに", "すべてすでに期日がありません"
	}
	return m.ask(&bulkPlan{tasks: change, question: "の" + label + "します",
		done: label + "しました", apply: updateAll(app.TaskUpdate{Due: &due})}, unchanged)
}

// sectionIn is the section of project that t is in ("" if none).
func sectionIn(t readmodel.TaskSummary, project string) string {
	for _, ms := range t.Memberships {
		if ms.ProjectGID == project {
			return ms.SectionGID
		}
	}
	return ""
}

func (m *Model) updateBulk(msg tea.Msg) (tea.Cmd, bool) {
	if msg, ok := msg.(bulkUsersMsg); ok {
		if msg.err != nil {
			m.status = "メンバーを読み込めませんでした: " + termsafe.String(msg.err.Error())
		} else {
			m.showBulkAssignee(msg)
		}
		return nil, true
	}
	return nil, false
}

// markGutter is the mark shown before a task in the list while some tasks are marked.
func (m *Model) markGutter(t *readmodel.TaskSummary) (string, bool) {
	if len(m.list.marked) == 0 {
		return "", false
	}
	if m.list.marked[t.GID] {
		return m.st.footerKey.Render("●") + " ", true
	}
	return "  ", true
}
