package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/ui/format"
)

// Goals and portfolios (#7), read only: space Q opens the goals, space l my portfolios
// (tab switches). They are read from the cache and refreshed from Asana when shown
// online. enter opens a goal (its subgoals and supporting work) or a portfolio (its
// projects); from there enter opens a subgoal, a portfolio, a task, or jumps to a
// project. A feature Asana refused with a 402 is not offered (planOff).

// goalScreen is the state of the goals / portfolios screen.
type goalScreen struct {
	portfolios bool // portfolios rather than goals
	current    bool // goals narrowed to the periods that include today

	list *readmodel.GoalList
	pfs  []readmodel.PortfolioView
	// pfsFetched: the portfolios were fetched at least once ("" never: nothing to tell).
	pfsFetched string

	// the goal or portfolio open (nil: the list), and those it was opened from
	goal  *readmodel.GoalDetail
	pf    *readmodel.PortfolioDetail
	stack []goalPage

	cursor int
	offset int
	note   string // why the screen shows nothing, or that it is the cache
	// seq tells the answers of the latest load apart; fetched: its fetched answer came
	// (a cached answer arriving later is older).
	seq     int
	fetched bool
}

// goalPage is a goal or a portfolio to come back to with esc.
type goalPage struct {
	goal, portfolio string // gid
	cursor          int
}

type goalListMsg struct {
	seq     int
	fetched bool
	list    *readmodel.GoalList
	pfs     []readmodel.PortfolioView
	pfsAt   string
	err     error
	fetch   error
}

type goalDetailMsg struct {
	seq     int
	fetched bool
	goal    *readmodel.GoalDetail
	pf      *readmodel.PortfolioDetail
	err     error
	fetch   error
}

func (m *Model) goalUseCases() *app.Goals { return &app.Goals{Store: m.deps.Store, Clock: m.deps.Now} }

func (m *Model) openGoals() tea.Cmd { return m.openGoalScreen(false) }

func (m *Model) openPortfolios() tea.Cmd { return m.openGoalScreen(true) }

func (m *Model) openGoalScreen(portfolios bool) tea.Cmd {
	g := &m.goals
	if !m.showing(overlayGoals) || g.portfolios != portfolios {
		g.cursor, g.offset = 0, 0
	}
	m.openOverlay(overlayGoals)
	g.portfolios = portfolios
	g.goal, g.pf, g.stack = nil, nil, nil
	return m.loadGoalList(true)
}

// loadGoalList shows the cached list, then (if fetch and online) refreshes it from Asana
// and shows it again.
func (m *Model) loadGoalList(fetch bool) tea.Cmd {
	g := &m.goals
	g.seq++
	g.fetched = false
	seq, portfolios, current := g.seq, g.portfolios, g.current
	uc, syncer, ctx := m.goalUseCases(), m.syncer, m.ctx
	load := func(fetched bool, ferr error) tea.Msg {
		msg := goalListMsg{seq: seq, fetched: fetched, fetch: ferr}
		if portfolios {
			msg.pfs, msg.err = uc.Portfolios(ctx)
			msg.pfsAt, _ = uc.PortfoliosFetchedAt(ctx)
		} else {
			period := ""
			if current {
				period = app.PeriodCurrent
			}
			msg.list, msg.err = uc.List(ctx, period)
		}
		return msg
	}
	cached := func() tea.Msg { return load(false, nil) }
	if !fetch || syncer == nil {
		return cached
	}
	return tea.Batch(cached, func() tea.Msg {
		var err error
		if portfolios {
			err = syncer.FetchPortfolios(ctx)
		} else {
			err = syncer.FetchGoals(ctx)
		}
		return load(true, err)
	})
}

// loadGoalDetail shows a cached goal or portfolio, then refreshes it from Asana.
func (m *Model) loadGoalDetail(page goalPage, fetch bool) tea.Cmd {
	g := &m.goals
	g.seq++
	g.fetched = false
	seq := g.seq
	uc, syncer, ctx := m.goalUseCases(), m.syncer, m.ctx
	load := func(fetched bool, ferr error) tea.Msg {
		msg := goalDetailMsg{seq: seq, fetched: fetched, fetch: ferr}
		if page.portfolio != "" {
			msg.pf, msg.err = uc.Portfolio(ctx, page.portfolio)
		} else {
			msg.goal, msg.err = uc.Get(ctx, page.goal)
		}
		return msg
	}
	cached := func() tea.Msg { return load(false, nil) }
	if !fetch || syncer == nil {
		return cached
	}
	return tea.Batch(cached, func() tea.Msg {
		var err error
		if page.portfolio != "" {
			err = syncer.FetchPortfolio(ctx, page.portfolio)
		} else {
			err = syncer.FetchGoal(ctx, page.goal)
		}
		return load(true, err)
	})
}

// fresh reports whether an answer of a load is to be shown: it is of the latest load, and
// not a cached answer arriving after the fetched one.
func (g *goalScreen) fresh(seq int, fetched bool) bool {
	if seq != g.seq || (!fetched && g.fetched) {
		return false
	}
	if fetched {
		g.fetched = true
	}
	return true
}

func (m *Model) updateGoals(msg tea.Msg) (tea.Cmd, bool) {
	g := &m.goals
	switch msg := msg.(type) {
	case goalListMsg:
		if !g.fresh(msg.seq, msg.fetched) {
			return nil, true
		}
		g.note = ""
		if msg.err != nil {
			g.list, g.pfs, g.note = nil, nil, msg.err.Error()
			if app.IsPlanUnavailable(msg.err) {
				return m.loadPlan(), true
			}
			return nil, true
		}
		g.list, g.pfs, g.pfsFetched = msg.list, msg.pfs, msg.pfsAt
		if msg.fetch != nil {
			g.note = "Asana から取れなかったため、キャッシュの内容です: " + msg.fetch.Error()
		}
		g.cursor = clamp(g.cursor, 0, max(0, m.goalRows()-1))
		return nil, true
	case goalDetailMsg:
		if !g.fresh(msg.seq, msg.fetched) {
			return nil, true
		}
		g.note = ""
		if msg.err != nil {
			g.note = msg.err.Error()
			if app.IsPlanUnavailable(msg.err) {
				return m.loadPlan(), true
			}
			return nil, true
		}
		g.goal, g.pf = msg.goal, msg.pf
		if msg.fetch != nil {
			g.note = "Asana から取れなかったため、キャッシュの内容です: " + msg.fetch.Error()
		}
		g.cursor = clamp(g.cursor, 0, max(0, m.goalRows()-1))
		return nil, true
	}
	return nil, false
}

// detailOpen reports whether a goal or a portfolio is open (rather than a list), or is
// being loaded.
func (g *goalScreen) detailOpen() bool { return g.goal != nil || g.pf != nil || len(g.stack) > 0 }

// goalRows is how many rows the cursor moves over.
func (m *Model) goalRows() int {
	g := &m.goals
	switch {
	case g.goal != nil:
		return len(g.goal.Supporting)
	case g.pf != nil:
		return len(g.pf.Items)
	case g.detailOpen():
		return 0
	case g.portfolios:
		return len(g.pfs)
	case g.list != nil:
		return len(g.list.Goals)
	}
	return 0
}

func (m *Model) handleGoalKey(key string) (tea.Model, tea.Cmd) {
	g := &m.goals
	n := m.goalRows()
	switch key {
	case "esc", "q", "backspace", "h", "left":
		if len(g.stack) > 0 {
			top := g.stack[len(g.stack)-1]
			g.stack = g.stack[:len(g.stack)-1]
			g.cursor, g.offset = top.cursor, 0
			if len(g.stack) == 0 && top.goal == "" && top.portfolio == "" {
				g.goal, g.pf = nil, nil
				return m, m.loadGoalList(false)
			}
			g.goal, g.pf = nil, nil
			return m, m.loadGoalDetail(top, false)
		}
		m.closeOverlay(overlayGoals)
	case "j", "down":
		g.cursor = min(g.cursor+1, max(0, n-1))
	case "k", "up":
		g.cursor = max(0, g.cursor-1)
	case "g", "home":
		g.cursor = 0
	case "G", "end":
		g.cursor = max(0, n-1)
	case "ctrl+d", "pgdown":
		g.cursor = min(g.cursor+10, max(0, n-1))
	case "ctrl+u", "pgup":
		g.cursor = max(0, g.cursor-10)
	case "tab":
		if g.detailOpen() {
			return m, nil
		}
		other := !g.portfolios
		if (other && !m.planOK(plan.Portfolios)) || (!other && !m.planOK(plan.Goals)) {
			return m, nil
		}
		return m, m.openGoalScreen(other)
	case "c":
		if g.detailOpen() || g.portfolios {
			return m, nil
		}
		g.current = !g.current
		g.cursor, g.offset = 0, 0
		return m, m.loadGoalList(false)
	case "r":
		if g.goal != nil {
			return m, m.loadGoalDetail(goalPage{goal: g.goal.GID}, true)
		}
		if g.pf != nil {
			return m, m.loadGoalDetail(goalPage{portfolio: g.pf.GID}, true)
		}
		return m, m.loadGoalList(true)
	case "enter", "l", "right":
		return m, m.openGoalRow()
	}
	return m, nil
}

// openGoalRow opens what the cursor is on.
func (m *Model) openGoalRow() tea.Cmd {
	g := &m.goals
	if g.cursor >= m.goalRows() {
		return nil
	}
	here := goalPage{cursor: g.cursor}
	if g.goal != nil {
		here.goal = g.goal.GID
	} else if g.pf != nil {
		here.portfolio = g.pf.GID
	}
	var kind, gid, name string
	switch {
	case g.goal != nil:
		s := g.goal.Supporting[g.cursor]
		kind, gid, name = s.Type, s.GID, s.Name
	case g.pf != nil:
		it := g.pf.Items[g.cursor]
		kind, gid, name = it.Type, it.GID, it.Name
	case g.portfolios:
		kind, gid = "portfolio", g.pfs[g.cursor].GID
	default:
		kind, gid = "goal", g.list.Goals[g.cursor].GID
	}
	switch kind {
	case "goal", "portfolio":
		g.stack = append(g.stack, here)
		g.goal, g.pf = nil, nil
		g.cursor, g.offset = 0, 0
		page := goalPage{goal: gid}
		if kind == "portfolio" {
			page = goalPage{portfolio: gid}
		}
		return m.loadGoalDetail(page, true)
	case "project":
		for i, s := range m.scopes {
			if s.kind == scopeProject && s.project == gid {
				m.closeOverlay(overlayGoals)
				return m.moveScope(i - m.scopeCursor)
			}
		}
		m.status = "プロジェクト「" + name + "」は同期していません"
	case "task":
		m.closeOverlay(overlayGoals)
		return m.openTaskDetail(gid)
	}
	return nil
}

// goalBadge is a goal's status, colored.
func (m *Model) goalBadge(status string) string {
	label := "[" + format.GoalStatusLabel(status) + "]"
	switch status {
	case "green", "achieved":
		return m.st.tag.Render(label)
	case "yellow", "partial":
		return m.st.today.Render(label)
	case "red", "missed":
		return m.st.overdue.Render(label)
	}
	return m.st.dim.Render(label)
}

// metricBar is "■■■□□□ 40 / 100（40%）", or the text alone when the progress is unknown.
func metricBar(mt *readmodel.GoalMetric, width int) string {
	text := format.GoalMetricText(mt)
	p, ok := mt.Progress()
	if !ok {
		return text
	}
	done := int(p * float64(width))
	return strings.Repeat("■", done) + strings.Repeat("□", width-done) + " " + text
}

func (m *Model) renderGoals(w, h int) string {
	g := &m.goals
	var title string
	var lines []string
	cursorLine := -1
	row := func(i int, s string) {
		if i == g.cursor {
			cursorLine = len(lines)
			s = m.st.selected.Render(ansi.Truncate(s, max(1, w-4), "…"))
		}
		lines = append(lines, s)
	}
	field := func(label, v string) {
		if v != "" {
			lines = append(lines, m.st.dim.Render(label+": ")+v)
		}
	}
	switch {
	case g.goal != nil:
		d := g.goal
		title = "ゴール（" + d.Name + "）"
		lines = append(lines, m.st.dim.Render("j/k 選ぶ  enter 開く（サブゴール・ポートフォリオ・タスク・プロジェクトへ）  r 取り直す  esc 戻る"), "")
		field("ステータス", m.goalBadge(d.Status))
		if d.Metric != nil {
			field("進み具合", metricBar(d.Metric, max(10, min(30, w-30))))
		}
		if d.TimePeriod != nil {
			field("期間", d.TimePeriod.DisplayName)
		}
		if d.StartOn != "" || d.DueOn != "" {
			field("日付", strings.TrimSpace(d.StartOn+" 〜 "+d.DueOn))
		}
		if d.Owner != nil {
			field("オーナー", d.Owner.Name)
		}
		if d.WorkspaceLevel {
			field("範囲", "ワークスペース全体")
		} else {
			field("チーム", d.TeamName)
		}
		if notes := m.markdown(textMarkdown(d.HTMLNotes, d.Notes), max(10, w-6)); notes != "" {
			lines = append(lines, "", m.st.headerScope.Render("説明"))
			lines = append(lines, strings.Split(notes, "\n")...)
		}
		lines = append(lines, "", m.st.headerScope.Render("支えているもの"))
		switch {
		case d.FetchedAt == "":
			lines = append(lines, m.st.dim.Render("（オンラインのときに取得します）"))
		case len(d.Supporting) == 0:
			lines = append(lines, m.st.dim.Render("（ありません）"))
		}
		for i, s := range d.Supporting {
			row(i, supportKind(s.Type)+"  "+s.Name)
		}
	case g.pf != nil:
		p := g.pf
		title = "ポートフォリオ（" + p.Name + "）"
		lines = append(lines, m.st.dim.Render("j/k 選ぶ  enter 開く（プロジェクトへ移る・ポートフォリオを開く）  r 取り直す  esc 戻る"), "")
		if p.Status != nil {
			field("最新のステータス", m.statusBadge(p.Status.StatusType)+" "+p.Status.Title)
		}
		if p.Owner != nil {
			field("オーナー", p.Owner.Name)
		}
		if p.StartOn != "" || p.DueOn != "" {
			field("期間", strings.TrimSpace(p.StartOn+" 〜 "+p.DueOn))
		}
		lines = append(lines, "")
		switch {
		case p.FetchedAt == "":
			lines = append(lines, m.st.dim.Render("（中身はオンラインのときに取得します）"))
		case len(p.Items) == 0:
			lines = append(lines, m.st.dim.Render("（中身はありません）"))
		}
		for i, it := range p.Items {
			s := supportKind(it.Type) + "  "
			if it.Status != nil {
				s += m.statusBadge(it.Status.StatusType) + " "
			}
			s += it.Name
			if it.Archived {
				s += m.st.dim.Render("（アーカイブ済み）")
			}
			if it.DueOn != "" {
				s += m.st.dim.Render("  期日 " + it.DueOn)
			}
			if it.Owner != nil {
				s += m.st.dim.Render("  " + it.Owner.Name)
			}
			row(i, s)
		}
	case g.detailOpen():
		title = "ゴール・ポートフォリオ"
		lines = append(lines, m.st.dim.Render("esc 戻る"), "")
		if g.note == "" {
			lines = append(lines, m.st.dim.Render("読み込んでいます…"))
		}
	case g.portfolios:
		title = "ポートフォリオ（自分がオーナーのもの）"
		help := "j/k 選ぶ  enter 開く  r 取り直す  esc 閉じる"
		if m.planOK(plan.Goals) {
			help = "j/k 選ぶ  enter 開く  tab ゴール  r 取り直す  esc 閉じる"
		}
		lines = append(lines, m.st.dim.Render(help), "")
		if len(g.pfs) == 0 && g.note == "" {
			if g.pfsFetched == "" {
				lines = append(lines, m.st.dim.Render("（オンラインのときに取得します）"))
			} else {
				lines = append(lines, m.st.dim.Render("（ポートフォリオはありません。Asana の API では自分がオーナーのものだけが見えます）"))
			}
		}
		for i, p := range g.pfs {
			s := ""
			if p.Status != nil {
				s = m.statusBadge(p.Status.StatusType) + " "
			}
			s += p.Name
			if p.DueOn != "" {
				s += m.st.dim.Render("  期日 " + p.DueOn)
			}
			row(i, s)
		}
	default:
		title = "ゴール"
		if g.current {
			title = "ゴール（今日を含む期間）"
		}
		help := "j/k 選ぶ  enter 開く  c 今の期間だけ / すべて  r 取り直す  esc 閉じる"
		if m.planOK(plan.Portfolios) {
			help = "j/k 選ぶ  enter 開く  c 今の期間だけ / すべて  tab ポートフォリオ  r 取り直す  esc 閉じる"
		}
		lines = append(lines, m.st.dim.Render(help), "")
		if g.list == nil {
			if g.note == "" {
				lines = append(lines, m.st.dim.Render("読み込んでいます…"))
			}
			break
		}
		if len(g.list.Goals) == 0 && g.note == "" {
			if g.list.FetchedAt == "" {
				lines = append(lines, m.st.dim.Render("（オンラインのときに取得します）"))
			} else {
				lines = append(lines, m.st.dim.Render("（ゴールはありません）"))
			}
		}
		barW := max(6, min(12, w/8))
		for i, gl := range g.list.Goals {
			s := m.goalBadge(gl.Status) + " " + gl.Name
			if gl.Metric != nil {
				s += "  " + m.st.dim.Render(metricBar(gl.Metric, barW))
			}
			if gl.TimePeriod != nil {
				s += m.st.dim.Render("  " + gl.TimePeriod.DisplayName)
			}
			if gl.Owner != nil {
				s += m.st.dim.Render("  " + gl.Owner.Name)
			}
			row(i, s)
		}
	}
	if g.note != "" {
		lines = append(lines[:min(2, len(lines))], append([]string{m.st.today.Render(g.note), ""}, lines[min(2, len(lines)):]...)...)
		if cursorLine >= 2 {
			cursorLine += 2
		}
	}
	// keep the cursor in view
	view := max(1, h-3)
	if cursorLine >= 0 {
		if cursorLine < g.offset {
			g.offset = cursorLine
		} else if cursorLine >= g.offset+view {
			g.offset = cursorLine - view + 1
		}
	}
	g.offset = clamp(g.offset, 0, max(0, len(lines)-view))
	return m.pane(title, lines[g.offset:], w, h, true)
}

// supportKind names the kind of a goal's supporting work or a portfolio's item.
func supportKind(t string) string {
	switch t {
	case "goal":
		return "サブゴール"
	case "project":
		return "プロジェクト"
	case "portfolio":
		return "ポートフォリオ"
	case "task":
		return "タスク"
	}
	return t
}
