package tui

import (
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/sync"
)

// Templates (#2): making a task or a project from a template, and duplicating a project
// (online only).
//
// The templates are not cached in the store. They are fetched when the menu opens and
// after each sync (at most every templateTTL), so the menu offers "from a template" only
// when there is a template to choose: none (the plan lacks them, or none were made) hides
// it, and so does a 402 in the workspace: for task templates, the project's (no list is
// fetched while the mark counts); for project templates, the default one (planOff).

const templateTTL = 10 * time.Minute

// templateState is what the TUI knows about templates.
type templateState struct {
	task     map[string]taskTemplates // by project gid
	project  projectTemplates
	fetching map[string]bool // "task:<project>" / "project"
	// chosen is the project template whose project's name is being asked for
	// (promptTemplateProject).
	chosen model.ProjectTemplate
	// dupProject is the project being duplicated (promptDuplicateProject).
	dupProject string
}

type taskTemplates struct {
	list []model.TaskTemplate
	at   time.Time
}

type projectTemplates struct {
	list []model.ProjectTemplate
	at   time.Time
}

type (
	taskTemplatesMsg struct {
		project string
		list    []model.TaskTemplate
		err     error
		open    bool // the user asked for them: show the picker
	}
	projectTemplatesMsg struct {
		list []model.ProjectTemplate
		err  error
		open bool
	}
)

// hasTaskTemplates reports whether the current project is known to have task templates.
func (m *Model) hasTaskTemplates() bool {
	return len(m.tmpl.task[m.currentProject()].list) > 0
}

func (m *Model) hasProjectTemplates() bool { return len(m.tmpl.project.list) > 0 }

// prefetchTemplates fetches the templates that are not known or are older than
// templateTTL: the current project's task templates and the project templates.
func (m *Model) prefetchTemplates() tea.Cmd {
	if m.syncer == nil {
		return nil
	}
	now := m.deps.Now()
	var cmds []tea.Cmd
	if p := m.currentProject(); p != "" {
		if c, ok := m.tmpl.task[p]; !ok || now.Sub(c.at) >= templateTTL {
			cmds = append(cmds, m.fetchTaskTemplates(p, false))
		}
	}
	if m.planOK(plan.ProjectTemplates) && (m.tmpl.project.at.IsZero() || now.Sub(m.tmpl.project.at) >= templateTTL) {
		cmds = append(cmds, m.fetchProjectTemplates(false))
	}
	return tea.Batch(cmds...)
}

func (m *Model) fetchTaskTemplates(project string, open bool) tea.Cmd {
	key := "task:" + project
	if m.syncer == nil || (m.tmpl.fetching[key] && !open) {
		return nil
	}
	if m.tmpl.fetching == nil {
		m.tmpl.fetching = map[string]bool{}
	}
	m.tmpl.fetching[key] = true
	syncer, ctx, st, now := m.syncer, m.ctx, m.deps.App, m.deps.Now()
	return func() tea.Msg {
		// A 402 in the project's workspace less than a day ago: not asked again yet.
		if ws, _ := st.Query().ProjectWorkspace(ctx, project); !open && !st.Query().TemplatesOffered(ctx, plan.TaskTemplates, ws, now) {
			return taskTemplatesMsg{project: project, err: &sync.TemplatePlanError{Feature: plan.TaskTemplates}, open: open}
		}
		list, err := syncer.TaskTemplates(ctx, project)
		return taskTemplatesMsg{project: project, list: list, err: err, open: open}
	}
}

func (m *Model) fetchProjectTemplates(open bool) tea.Cmd {
	if m.syncer == nil || (m.tmpl.fetching["project"] && !open) {
		return nil
	}
	if m.tmpl.fetching == nil {
		m.tmpl.fetching = map[string]bool{}
	}
	m.tmpl.fetching["project"] = true
	syncer, ctx := m.syncer, m.ctx
	return func() tea.Msg {
		list, err := syncer.ProjectTemplates(ctx, "")
		return projectTemplatesMsg{list: list, err: err, open: open}
	}
}

// startTaskFromTemplate shows the current project's task templates to choose from.
func (m *Model) startTaskFromTemplate() tea.Cmd {
	if _, ok := m.online(); !ok {
		return nil
	}
	p := m.currentProject()
	if c, ok := m.tmpl.task[p]; ok && len(c.list) > 0 && m.deps.Now().Sub(c.at) < templateTTL {
		m.showTaskTemplates(p, c.list)
		return nil
	}
	m.status = "タスクテンプレートを取っています…"
	return m.fetchTaskTemplates(p, true)
}

func (m *Model) startProjectFromTemplate() tea.Cmd {
	if _, ok := m.online(); !ok {
		return nil
	}
	if c := m.tmpl.project; len(c.list) > 0 && m.deps.Now().Sub(c.at) < templateTTL {
		m.showProjectTemplates(c.list)
		return nil
	}
	m.status = "プロジェクトテンプレートを取っています…"
	return m.fetchProjectTemplates(true)
}

func (m *Model) showTaskTemplates(project string, list []model.TaskTemplate) {
	items := make([]pickerItem, 0, len(list))
	for _, t := range list {
		items = append(items, pickerItem{label: t.Name, value: t.GID})
	}
	m.picker = &picker{kind: pickTaskTemplate, title: "テンプレートからタスクを作る", items: items, project: project}
}

func (m *Model) showProjectTemplates(list []model.ProjectTemplate) {
	items := make([]pickerItem, 0, len(list))
	for _, t := range list {
		var hint []string
		if t.Team != nil && t.Team.Name != "" {
			hint = append(hint, t.Team.Name)
		}
		if len(t.RequestedDates) > 0 {
			hint = append(hint, "日付は今日から")
		}
		items = append(items, pickerItem{label: t.Name, value: t.GID, hint: strings.Join(hint, "・")})
	}
	m.picker = &picker{kind: pickProjectTemplate, title: "テンプレートからプロジェクトを作る", items: items}
}

// makeTaskFromTemplate makes a task from the chosen template (named after it).
func (m *Model) makeTaskFromTemplate(project, gid string) tea.Cmd {
	syncer, ok := m.online()
	if !ok {
		return nil
	}
	var tmpl model.TaskTemplate
	for _, t := range m.tmpl.task[project].list {
		if t.GID == gid {
			tmpl = t
		}
	}
	if tmpl.GID == "" {
		return nil
	}
	if tmpl.Project == nil {
		tmpl.Project = &model.Ref{GID: project}
	}
	m.status = "「" + tmpl.Name + "」からタスクを作っています…"
	ctx := m.ctx
	return func() tea.Msg {
		made, err := syncer.InstantiateTask(ctx, tmpl, "")
		if made == nil {
			return writeDoneMsg{err: err}
		}
		status := "タスクを作りました: " + made.Name
		if made.Pending {
			status += "（サブタスクは Asana が作っている途中です。タスクを開くと取り込みます）"
		}
		if err != nil {
			status += "（" + err.Error() + "）"
		}
		return writeDoneMsg{status: status, gid: made.GID}
	}
}

// chooseProjectTemplate asks for the name of the project made from template gid.
func (m *Model) chooseProjectTemplate(gid string) tea.Cmd {
	for _, t := range m.tmpl.project.list {
		if t.GID == gid {
			m.tmpl.chosen = t
			m.openPrompt(promptTemplateProject)
			m.prompt.input.SetValue(t.Name)
			m.prompt.input.CursorEnd()
			return m.prompt.input.Focus()
		}
	}
	return nil
}

func (m *Model) makeProjectFromTemplate(name string) tea.Cmd {
	syncer, ok := m.online()
	tmpl := m.tmpl.chosen
	if !ok || tmpl.GID == "" {
		return nil
	}
	m.status = "「" + tmpl.Name + "」からプロジェクトを作っています…"
	ctx := m.ctx
	return func() tea.Msg {
		return madeProject(syncer.InstantiateProject(ctx, tmpl, sync.ProjectInput{Name: name}))
	}
}

func (m *Model) startDuplicateProject() tea.Cmd {
	if _, ok := m.online(); !ok {
		return nil
	}
	p := m.currentProject()
	if p == "" {
		return nil
	}
	m.tmpl.dupProject = p
	m.openPrompt(promptDuplicateProject)
	m.prompt.input.SetValue(m.currentScope().name + " のコピー")
	m.prompt.input.CursorEnd()
	return m.prompt.input.Focus()
}

func (m *Model) duplicateProject(name string) tea.Cmd {
	syncer, ok := m.online()
	project := m.tmpl.dupProject
	if !ok || project == "" {
		return nil
	}
	m.status = "プロジェクトを複製しています…"
	ctx := m.ctx
	return func() tea.Msg {
		return madeProject(syncer.DuplicateProject(ctx, project, name, ""))
	}
}

func madeProject(made *sync.Made, err error) tea.Msg {
	if made == nil {
		return projectDoneMsg{err: err}
	}
	status := "プロジェクトを作りました: " + made.Name
	if made.Pending {
		status += "（中身は Asana が作っている途中です。次の同期で取り込みます）"
	}
	if err != nil {
		status += "（" + err.Error() + "）"
	}
	return projectDoneMsg{status: status, selectProject: made.GID}
}

// updateTemplates handles the messages of this file.
func (m *Model) updateTemplates(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case taskTemplatesMsg:
		delete(m.tmpl.fetching, "task:"+msg.project)
		if m.tmpl.task == nil {
			m.tmpl.task = map[string]taskTemplates{}
		}
		if m.templateError(msg.err, plan.TaskTemplates, msg.open) {
			// Not looked for again before templateTTL; what was known is kept.
			c := m.tmpl.task[msg.project]
			m.tmpl.task[msg.project] = taskTemplates{list: c.list, at: m.deps.Now()}
			return nil, true
		}
		m.tmpl.task[msg.project] = taskTemplates{list: msg.list, at: m.deps.Now()}
		if msg.open {
			if len(msg.list) == 0 {
				m.status = "このプロジェクトにはタスクテンプレートがありません"
				return nil, true
			}
			m.status = ""
			m.showTaskTemplates(msg.project, msg.list)
		}
		return nil, true
	case projectTemplatesMsg:
		delete(m.tmpl.fetching, "project")
		if m.templateError(msg.err, plan.ProjectTemplates, msg.open) {
			m.tmpl.project.at = m.deps.Now() // not looked for again before templateTTL
			var pe *sync.TemplatePlanError
			if errors.As(msg.err, &pe) {
				return m.loadPlan(), true
			}
			return nil, true
		}
		m.tmpl.project = projectTemplates{list: msg.list, at: m.deps.Now()}
		if msg.open {
			if len(msg.list) == 0 {
				m.status = "プロジェクトテンプレートがありません（プロジェクトの複製はできます）"
				return nil, true
			}
			m.status = ""
			m.showProjectTemplates(msg.list)
		}
		return nil, true
	}
	return nil, false
}

// templateError handles a failed fetch: a 402 hides the feature; other errors are shown
// only when the user asked (open). It reports whether there was an error.
func (m *Model) templateError(err error, feature string, open bool) bool {
	if err == nil {
		return false
	}
	var pe *sync.TemplatePlanError
	// Task templates are hidden per project (no list), project templates by planOff.
	if errors.As(err, &pe) && feature == plan.ProjectTemplates {
		if m.planOff == nil {
			m.planOff = map[string]planWindow{}
		}
		at := m.now().Truncate(time.Second)
		from, until, _ := plan.Window(at, at)
		m.planOff[feature] = planWindow{from: from, until: until}
	}
	if open {
		m.status = err.Error()
	}
	return true
}
