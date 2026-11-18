package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/project"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
)

// The project overview (O): the project's description, owner, dates, color, progress,
// latest status and brief. It is read from the cache and refreshed from Asana when online;
// e edits the fields in the editor (queued like any other change, offline too).

type overviewLoadedMsg struct {
	project  string
	overview *readmodel.ProjectOverview
	err      error
	// fetchErr is why the refresh from Asana failed (the overview is the cached one).
	fetchErr error
}

func (m *Model) openOverview() tea.Cmd {
	project := m.currentProject()
	if project == "" {
		return nil
	}
	m.overviewView = true
	m.overviewOffset = 0
	if m.overview != nil && m.overview.GID != project {
		m.overview = nil
	}
	return m.loadOverview(project, true)
}

// loadOverview shows the cached overview, then (if fetch and online) refreshes it from
// Asana and shows it again.
func (m *Model) loadOverview(project string, fetch bool) tea.Cmd {
	st, syncer := m.deps.Store, m.syncer
	load := func() overviewLoadedMsg {
		o, err := queryOf(st).ProjectOverview(m.ctx, project)
		return overviewLoadedMsg{project: project, overview: o, err: err}
	}
	cached := func() tea.Msg { return load() }
	if !fetch || syncer == nil {
		return cached
	}
	return tea.Batch(cached, func() tea.Msg {
		_ = syncer.FetchStatusUpdates(m.ctx, project)
		err := syncer.FetchProjectOverview(m.ctx, project)
		msg := load()
		msg.fetchErr = err
		return msg
	})
}

func (m *Model) handleOverviewKey(key string) (tea.Model, tea.Cmd) {
	if m.picker != nil { // the confirmation of B, over the overview
		return m.handlePickerKey(key)
	}
	switch key {
	case "esc", "q", "O":
		m.overviewView = false
	case "j", "down":
		m.overviewOffset++
	case "k", "up":
		m.overviewOffset = max(0, m.overviewOffset-1)
	case "g", "home":
		m.overviewOffset = 0
	case "ctrl+d", "pgdown":
		m.overviewOffset += 10
	case "ctrl+u", "pgup":
		m.overviewOffset = max(0, m.overviewOffset-10)
	case "e":
		return m, m.editProject()
	case "b":
		return m, m.editBrief()
	case "B":
		return m, m.confirmDeleteBrief()
	case "U":
		m.overviewView = false
		return m, m.openStatuses()
	case "W":
		m.overviewView = false
		return m, m.writeStatus()
	}
	return m, nil
}

func (m *Model) renderOverview(w, h int) string {
	o := m.overview
	help := "j/k スクロール  e 編集（エディタ）  U ステータス更新  W ステータスを書く  esc 閉じる"
	if o != nil && o.BriefState != readmodel.BriefUnavailable { // Asana gives the brief
		help = "j/k スクロール  e 編集（エディタ）  b ブリーフを書く  B ブリーフを削除  U ステータス更新  W ステータスを書く  esc 閉じる"
	}
	lines := []string{m.st.dim.Render(help), ""}
	if o == nil {
		lines = append(lines, m.st.dim.Render("読み込んでいます…"))
		return m.pane("プロジェクトの概要（"+m.currentScope().name+"）", lines, w, h, true)
	}
	field := func(label, v string) {
		if v != "" {
			lines = append(lines, m.st.dim.Render(label+": ")+v)
		}
	}
	if o.Favorite {
		field("お気に入り", "★")
	}
	field("チーム", o.TeamName)
	if o.Owner != nil {
		field("オーナー", o.Owner.Name)
	} else {
		field("オーナー", m.st.dim.Render("（なし）"))
	}
	if o.StartOn != "" || o.DueOn != "" {
		field("期間", strings.TrimSpace(o.StartOn+" 〜 "+o.DueOn))
	}
	field("色", o.Color)
	if o.Counts != nil {
		field("進み具合", progressBar(o.Counts, max(10, min(30, w-30))))
	} else if o.FetchedAt == "" {
		field("進み具合", m.st.dim.Render("（オンラインのときに取得します）"))
	}
	if o.Status != nil {
		field("最新のステータス", m.statusBadge(o.Status.StatusType)+" "+o.Status.Title+"  "+
			m.st.dim.Render(o.Status.Author+"  "+commentTime(o.Status.CreatedAt, m.deps.Now())))
	}
	if o.Pending {
		lines = append(lines, m.st.today.Render("（変更は未送信です）"))
	}
	lines = append(lines, "", m.st.headerScope.Render("説明"))
	if notes := m.markdown(textMarkdown(o.HTMLNotes, o.Notes), max(10, w-6)); notes != "" {
		lines = append(lines, strings.Split(notes, "\n")...)
	} else {
		lines = append(lines, m.st.dim.Render("（説明はありません。e で書けます）"))
	}
	switch {
	case o.Brief != nil:
		title := "ブリーフ"
		if o.Brief.Title != "" {
			title += "「" + o.Brief.Title + "」"
		}
		lines = append(lines, "", m.st.headerScope.Render(title))
		if o.BriefPending {
			lines = append(lines, m.st.today.Render("（ブリーフの変更は未送信です）"))
		}
		lines = append(lines, strings.Split(m.markdown(textMarkdown(o.Brief.HTMLText, o.Brief.Text), max(10, w-6)), "\n")...)
	case o.BriefPending: // deleted here, not sent yet
		lines = append(lines, "", m.st.headerScope.Render("ブリーフ"), m.st.today.Render("（ブリーフの削除は未送信です）"))
	case o.BriefState == readmodel.BriefNone:
		lines = append(lines, "", m.st.headerScope.Render("ブリーフ"), m.st.dim.Render("（ブリーフはありません。b で書けます）"))
	}
	m.overviewOffset = clamp(m.overviewOffset, 0, max(0, len(lines)-(h-3)))
	return m.pane("プロジェクトの概要（"+o.Name+"）", lines[m.overviewOffset:], w, h, true)
}

// progressBar is "■■■□□□ 12 / 30 件完了（40%）" (and the milestones, if any).
func progressBar(c *readmodel.TaskCounts, width int) string {
	done := 0
	if c.Tasks > 0 {
		done = c.CompletedTasks * width / c.Tasks
	}
	s := strings.Repeat("■", done) + strings.Repeat("□", width-done) + fmt.Sprintf(" %d / %d 件完了", c.CompletedTasks, c.Tasks)
	if c.Tasks > 0 {
		s += fmt.Sprintf("（%d%%）", c.CompletedTasks*100/c.Tasks)
	}
	if c.Milestones > 0 {
		s += fmt.Sprintf("  マイルストーン %d / %d", c.CompletedMilestones, c.Milestones)
	}
	return s
}

// --- editing the project in the editor ---

// projectDoc is a project's fields as the editor shows them.
type projectDoc struct {
	Owner, StartOn, DueOn, Color, Body string
}

const projectHelp = `# asagao: プロジェクトの設定です。保存してエディタを閉じると変更します（オフラインでも積み、同期で送ります）。
# owner: me・ユーザー名・none（外す）
# start_on / due_on: 2026-12-01・today・+3d（空欄で外す。開始日には期日が必要）
# color: dark-… / light-…（pink green blue red teal brown orange purple warm-gray）・none
# 下の --- のあとが説明（マークダウン）です。`

func (d projectDoc) String() string {
	return "---\n" + projectHelp + "\nowner: " + d.Owner + "\nstart_on: " + d.StartOn + "\ndue_on: " + d.DueOn +
		"\ncolor: " + d.Color + "\n---\n\n" + d.Body + "\n"
}

func parseProjectDoc(s string) (projectDoc, error) {
	var d projectDoc
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return d, errors.New("先頭の --- が見つかりません")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return d, errors.New("項目の終わりの --- が見つかりません")
	}
	for _, l := range lines[1:end] {
		if strings.HasPrefix(strings.TrimSpace(l), "#") || strings.TrimSpace(l) == "" {
			continue
		}
		key, value, ok := strings.Cut(l, ":")
		if !ok {
			return d, fmt.Errorf("読めない行があります: %q", l)
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "owner":
			d.Owner = value
		case "start_on":
			d.StartOn = value
		case "due_on":
			d.DueOn = value
		case "color":
			d.Color = value
		default:
			return d, fmt.Errorf("知らない項目です: %q", strings.TrimSpace(key))
		}
	}
	d.Body = strings.TrimSpace(strings.Join(lines[end+1:], "\n"))
	return d, nil
}

// input is what changed from original, as the use case's input (nil: unchanged).
func (d projectDoc) input(original projectDoc) app.UpdateProjectInput {
	var in app.UpdateProjectInput
	changed := func(now, before string) *string {
		if now == before {
			return nil
		}
		return &now
	}
	in.Owner = changed(d.Owner, original.Owner)
	in.StartOn = changed(d.StartOn, original.StartOn)
	in.DueOn = changed(d.DueOn, original.DueOn)
	in.Color = changed(d.Color, original.Color)
	in.Notes = changed(d.Body, strings.TrimSpace(original.Body))
	return in
}

type (
	projectEditReadyMsg struct {
		project, path string
		original      projectDoc
		err           error
	}
	projectEditDoneMsg struct {
		project, path string
		original      projectDoc
		err           error
	}
	projectEditedMsg struct {
		project, status string
		err             error
	}
)

func (m *Model) editProject() tea.Cmd {
	project := m.currentProject()
	if project == "" || m.deps.RunEditor == nil {
		return nil
	}
	st := m.deps.Store
	return func() tea.Msg {
		o, err := queryOf(st).ProjectOverview(m.ctx, project)
		if err != nil {
			return projectEditReadyMsg{err: err}
		}
		doc := projectDoc{StartOn: o.StartOn, DueOn: o.DueOn, Color: o.Color, Body: editableBody(o.HTMLNotes, o.Notes, nil)}
		if o.Owner != nil {
			doc.Owner = o.Owner.Name
			if me, _ := queryOf(st).MeGID(m.ctx); me == o.Owner.GID {
				doc.Owner = "me"
			}
		}
		path, err := writeTemp("asagao-project-*.md", doc.String())
		return projectEditReadyMsg{project: project, path: path, original: doc, err: err}
	}
}

func (m *Model) applyProjectEdit(msg projectEditDoneMsg) tea.Cmd {
	if msg.err != nil {
		m.status = "エディタがエラーで終了しました: " + msg.err.Error()
		return nil
	}
	data, err := os.ReadFile(msg.path)
	if err != nil {
		m.status = err.Error()
		return nil
	}
	doc, err := parseProjectDoc(string(data))
	if err != nil {
		m.status = "読めませんでした（" + err.Error() + "）。内容は " + msg.path + " に残っています"
		return nil
	}
	in := doc.input(msg.original)
	if in == (app.UpdateProjectInput{}) {
		os.Remove(msg.path)
		m.status = "変更はありません"
		return nil
	}
	projects := &app.Projects{Store: m.deps.Store, Text: richtext.Converter{}, Clock: m.deps.Now}
	path, gid := msg.path, msg.project
	return func() tea.Msg {
		u, err := projects.ResolveUpdate(m.ctx, in)
		if err == nil {
			err = projects.UpdateHere(m.ctx, gid, u)
		}
		if err != nil {
			err = fieldMessage(err)
			return projectEditedMsg{project: gid, err: fmt.Errorf("%w（内容は %s に残っています）", err, path)}
		}
		os.Remove(path)
		return projectEditedMsg{project: gid, status: "プロジェクトを変更しました"}
	}
}

// fieldMessage puts the field's label before the error inside an *app.FieldError.
func fieldMessage(err error) error {
	var fe *app.FieldError
	if errors.As(err, &fe) {
		if label := project.FieldLabel(fe.Field); label != "" {
			return fmt.Errorf("%s: %w", label, fe.Err)
		}
	}
	return err
}

func (m *Model) updateOverview(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case overviewLoadedMsg:
		if msg.project != m.currentProject() {
			return nil, true
		}
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil, true
		}
		m.overview = msg.overview
		if msg.fetchErr != nil && m.overviewView {
			m.status = "Asana から取れなかったため、一部はキャッシュの内容です: " + msg.fetchErr.Error()
		}
		return nil, true
	case projectEditReadyMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil, true
		}
		return m.deps.RunEditor(msg.path, func(err error) tea.Msg {
			return projectEditDoneMsg{project: msg.project, path: msg.path, original: msg.original, err: err}
		}), true
	case projectEditDoneMsg:
		return m.applyProjectEdit(msg), true
	case projectEditedMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
			return nil, true
		}
		m.status = msg.status
		return tea.Batch(m.afterWrite(), m.loadOverview(msg.project, false)), true
	case teamChoiceMsg:
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil, true
		}
		items := make([]pickerItem, 0, len(msg.teams))
		for _, t := range msg.teams {
			items = append(items, pickerItem{label: t.Name, value: t.GID})
		}
		m.newProjectName = msg.name
		m.picker = &picker{kind: pickTeam, title: "「" + msg.name + "」を作るチーム", items: items}
		return nil, true
	}
	return nil, false
}

// --- the team of a new project ---

// teamChoiceMsg asks which team a new project goes in (there are several).
type teamChoiceMsg struct {
	name  string
	teams []readmodel.TeamView
	err   error
}
