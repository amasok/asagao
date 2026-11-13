// Package tui is the Bubble Tea terminal UI. It reads everything from the local store, so it
// starts instantly and works offline; syncing with Asana runs in the background.
package tui

import (
	"context"
	"errors"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

// Deps are the TUI's external dependencies (swapped out in tests).
type Deps struct {
	Store *store.Store
	// NewSyncer creates a syncer with an authenticated API client. It is called lazily in the
	// background so the UI never waits for authentication (e.g. a 1Password unlock).
	// nil means offline-only.
	NewSyncer func(ctx context.Context) (*sync.Syncer, error)
	// SyncInterval is how often to sync in the background (0 = only on start and on "r").
	SyncInterval time.Duration
	OpenURL      func(url string) error
	Now          func() time.Time
}

type pane int

const (
	paneSidebar pane = iota
	paneList
	paneDetail
)

// filterMode is cycled with "f".
type filterMode int

const (
	filterOpen filterMode = iota
	filterDueWeek
	filterOverdue
	filterCompleted
	filterAll
	filterCount
)

func (f filterMode) String() string {
	return [...]string{"未完了", "7日以内が期限", "期限切れ", "完了済み", "すべて"}[f]
}

type scopeKind int

const (
	scopeMyTasks scopeKind = iota
	scopeProject
)

type scope struct {
	kind     scopeKind
	name     string
	project  string // gid
	sections []store.SectionView
}

// row is one line of the task list: a group header or a task.
type row struct {
	header string
	task   *store.TaskSummary
}

// detailRefreshAge: details (comments) older than this are re-fetched when a task is selected.
const detailRefreshAge = 5 * time.Minute

// narrowWidth: below this terminal width only the focused pane is shown.
const narrowWidth = 100

type Model struct {
	deps Deps
	ctx  context.Context
	st   styles

	width, height int
	focus         pane

	scopes      []scope
	scopeCursor int

	rows       []row
	listCursor int // index into rows; always on a task row when any exist
	listOffset int
	taskSeq    int // drops stale task loads

	detail     *store.TaskDetail
	detailView viewport.Model

	search    textinput.Model
	searching bool // input has focus
	query     string
	filter    filterMode

	syncer      *sync.Syncer
	syncing     bool
	syncErr     error
	lastSync    time.Time
	fetchingFor string // gid whose details are being fetched

	status   string // transient message for the footer
	showHelp bool
}

func New(ctx context.Context, deps Deps) *Model {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	ti := textinput.New()
	ti.Prompt = "/ "
	ti.Placeholder = "タスク名・説明・コメントを検索"
	m := &Model{
		deps:       deps,
		ctx:        ctx,
		st:         newStyles(true),
		focus:      paneList,
		scopes:     []scope{{kind: scopeMyTasks, name: "My Tasks"}},
		detailView: viewport.New(),
		search:     ti,
	}
	return m
}

// --- messages ---

type (
	scopesLoadedMsg struct {
		projects []store.ProjectView
		err      error
	}
	tasksLoadedMsg struct {
		seq  int
		rows []row
		err  error
	}
	detailLoadedMsg struct {
		gid string
		d   *store.TaskDetail
		err error
	}
	detailDebounceMsg struct{ gid string }
	detailFetchedMsg  struct {
		gid string
		err error
	}
	syncDoneMsg struct {
		syncer *sync.Syncer
		res    sync.Result
		err    error
		at     time.Time
	}
	syncTickMsg struct{}
	statusMsg   string
)

func (m *Model) Init() tea.Cmd {
	return tea.Batch(tea.RequestBackgroundColor, m.loadScopes(), m.loadTasks(), m.startSync())
}

// --- commands ---

func (m *Model) loadScopes() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		ps, err := st.ListProjects(m.ctx, true)
		return scopesLoadedMsg{projects: ps, err: err}
	}
}

func (m *Model) loadTasks() tea.Cmd {
	m.taskSeq++
	seq, sc, query, filter := m.taskSeq, m.currentScope(), m.query, m.filter
	st, now := m.deps.Store, m.deps.Now()
	return func() tea.Msg {
		rows, err := queryRows(m.ctx, st, sc, query, filter, now)
		return tasksLoadedMsg{seq: seq, rows: rows, err: err}
	}
}

func (m *Model) loadDetail(gid string) tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		d, err := st.GetTask(m.ctx, gid)
		return detailLoadedMsg{gid: gid, d: d, err: err}
	}
}

func (m *Model) startSync() tea.Cmd {
	if m.syncing || m.deps.NewSyncer == nil {
		return nil
	}
	m.syncing = true
	syncer, newSyncer, now := m.syncer, m.deps.NewSyncer, m.deps.Now
	return func() tea.Msg {
		if syncer == nil {
			s, err := newSyncer(m.ctx)
			if err != nil {
				return syncDoneMsg{err: err, at: now()}
			}
			syncer = s
		}
		res, err := syncer.Run(m.ctx, false)
		return syncDoneMsg{syncer: syncer, res: res, err: err, at: now()}
	}
}

func (m *Model) scheduleSync() tea.Cmd {
	if m.deps.SyncInterval <= 0 {
		return nil
	}
	return tea.Tick(m.deps.SyncInterval, func(time.Time) tea.Msg { return syncTickMsg{} })
}

func (m *Model) fetchDetails(gid string) tea.Cmd {
	if m.syncer == nil || m.fetchingFor == gid {
		return nil
	}
	m.fetchingFor = gid
	syncer := m.syncer
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 15*time.Second)
		defer cancel()
		return detailFetchedMsg{gid: gid, err: syncer.FetchDetails(ctx, gid)}
	}
}

func (m *Model) openURL(url string) tea.Cmd {
	open := m.deps.OpenURL
	if open == nil || url == "" {
		return nil
	}
	return func() tea.Msg {
		if err := open(url); err != nil {
			return statusMsg("ブラウザを開けませんでした: " + err.Error())
		}
		return statusMsg("ブラウザで開きました")
	}
}

// --- update ---

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.st = newStyles(msg.IsDark())
		m.refreshDetailView()
		return m, nil

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layoutDetail()
		return m, nil

	case scopesLoadedMsg:
		if msg.err != nil {
			m.status = "プロジェクトの読み込みに失敗: " + msg.err.Error()
			return m, nil
		}
		m.setProjects(msg.projects)
		return m, nil

	case tasksLoadedMsg:
		if msg.seq != m.taskSeq {
			return m, nil
		}
		if msg.err != nil {
			m.rows = nil
			m.status = msg.err.Error()
			return m, nil
		}
		return m, m.setRows(msg.rows)

	case detailLoadedMsg:
		if t := m.selectedTask(); t == nil || t.GID != msg.gid {
			return m, nil
		}
		if msg.err != nil {
			m.detail = nil
			m.status = msg.err.Error()
		} else {
			keepScroll := m.detail != nil && m.detail.GID == msg.gid
			m.detail = msg.d
			m.refreshDetailView()
			if !keepScroll {
				m.detailView.GotoTop()
			}
		}
		return m, nil

	case detailDebounceMsg:
		t := m.selectedTask()
		if t == nil || t.GID != msg.gid || !m.needsDetailFetch() {
			return m, nil
		}
		return m, m.fetchDetails(msg.gid)

	case detailFetchedMsg:
		if m.fetchingFor == msg.gid {
			m.fetchingFor = ""
		}
		if msg.err != nil {
			m.status = "詳細を取得できませんでした（オフライン表示）: " + msg.err.Error()
			return m, nil
		}
		if t := m.selectedTask(); t != nil && t.GID == msg.gid {
			return m, m.loadDetail(msg.gid)
		}
		return m, nil

	case syncDoneMsg:
		m.syncing = false
		m.syncErr = msg.err
		if msg.syncer != nil {
			m.syncer = msg.syncer
		}
		cmds := []tea.Cmd{m.scheduleSync()}
		if msg.err == nil {
			m.lastSync = msg.at
			cmds = append(cmds, m.loadScopes(), m.loadTasks())
			if t := m.selectedTask(); t != nil {
				cmds = append(cmds, m.loadDetail(t.GID), m.debounceDetail(t.GID))
			}
		}
		return m, tea.Batch(cmds...)

	case syncTickMsg:
		return m, m.startSync()

	case statusMsg:
		m.status = string(msg)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	if m.searching {
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		return m, tea.Quit
	}
	m.status = ""

	if m.searching {
		switch key {
		case "esc":
			m.searching = false
			m.search.Blur()
			m.search.Reset()
			if m.query != "" {
				m.query = ""
				return m, m.loadTasks()
			}
			return m, nil
		case "enter":
			m.searching = false
			m.search.Blur()
			m.focus = paneList
			return m, nil
		case "up", "down", "ctrl+n", "ctrl+p":
			// let the user move through results while typing
			if key == "up" || key == "ctrl+p" {
				return m, m.moveList(-1)
			}
			return m, m.moveList(1)
		}
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		if q := m.search.Value(); q != m.query {
			m.query = q
			return m, tea.Batch(cmd, m.loadTasks())
		}
		return m, cmd
	}

	if m.showHelp {
		m.showHelp = false
		return m, nil
	}

	switch key {
	case "?":
		m.showHelp = true
		return m, nil
	case "q":
		if m.narrow() && m.focus != paneSidebar {
			m.focus--
			return m, nil
		}
		return m, tea.Quit
	case "/":
		m.searching = true
		m.search.SetValue(m.query)
		m.search.CursorEnd()
		return m, m.search.Focus()
	case "esc":
		if m.query != "" {
			m.query = ""
			m.search.Reset()
			return m, m.loadTasks()
		}
		if m.focus == paneDetail {
			m.focus = paneList
		}
		return m, nil
	case "f":
		m.filter = (m.filter + 1) % filterCount
		return m, m.loadTasks()
	case "F":
		m.filter = (m.filter + filterCount - 1) % filterCount
		return m, m.loadTasks()
	case "r":
		if m.syncing {
			m.status = "同期中です"
			return m, nil
		}
		m.status = "同期しています…"
		return m, m.startSync()
	case "tab":
		m.focus = (m.focus + 1) % 3
		return m, nil
	case "shift+tab":
		m.focus = (m.focus + 2) % 3
		return m, nil
	case "o":
		if t := m.selectedTask(); t != nil && m.focus != paneSidebar {
			return m, m.openURL(t.PermalinkURL)
		}
		return m, nil
	}

	switch m.focus {
	case paneSidebar:
		switch key {
		case "j", "down":
			return m, m.moveScope(1)
		case "k", "up":
			return m, m.moveScope(-1)
		case "g", "home":
			return m, m.moveScope(-len(m.scopes))
		case "G", "end":
			return m, m.moveScope(len(m.scopes))
		case "enter", "l", "right":
			m.focus = paneList
		}
	case paneList:
		half := max(1, m.listHeight()/2)
		switch key {
		case "j", "down":
			return m, m.moveList(1)
		case "k", "up":
			return m, m.moveList(-1)
		case "ctrl+d", "pgdown":
			return m, m.moveList(half)
		case "ctrl+u", "pgup":
			return m, m.moveList(-half)
		case "g", "home":
			return m, m.moveList(-len(m.rows))
		case "G", "end":
			return m, m.moveList(len(m.rows))
		case "enter", "l", "right":
			if m.selectedTask() != nil {
				m.focus = paneDetail
			}
		case "h", "left":
			m.focus = paneSidebar
		}
	case paneDetail:
		switch key {
		case "j", "down":
			m.detailView.ScrollDown(1)
		case "k", "up":
			m.detailView.ScrollUp(1)
		case "ctrl+d", "pgdown", "space":
			m.detailView.HalfPageDown()
		case "ctrl+u", "pgup":
			m.detailView.HalfPageUp()
		case "g", "home":
			m.detailView.GotoTop()
		case "G", "end":
			m.detailView.GotoBottom()
		case "h", "left":
			m.focus = paneList
		}
	}
	return m, nil
}

// --- state helpers ---

func (m *Model) currentScope() scope {
	if m.scopeCursor < len(m.scopes) {
		return m.scopes[m.scopeCursor]
	}
	return m.scopes[0]
}

func (m *Model) setProjects(ps []store.ProjectView) {
	current := m.currentScope()
	scopes := []scope{{kind: scopeMyTasks, name: "My Tasks"}}
	for _, p := range ps {
		scopes = append(scopes, scope{kind: scopeProject, name: p.Name, project: p.GID, sections: p.Sections})
	}
	m.scopes = scopes
	m.scopeCursor = 0
	for i, s := range scopes {
		if s.kind == current.kind && s.project == current.project {
			m.scopeCursor = i
		}
	}
}

func (m *Model) moveScope(delta int) tea.Cmd {
	next := clamp(m.scopeCursor+delta, 0, len(m.scopes)-1)
	if next == m.scopeCursor {
		return nil
	}
	m.scopeCursor = next
	m.listCursor, m.listOffset = 0, 0
	return m.loadTasks()
}

// setRows replaces the list, keeping the selected task if it is still there.
func (m *Model) setRows(rows []row) tea.Cmd {
	var prev string
	if t := m.selectedTask(); t != nil {
		prev = t.GID
	}
	m.rows = rows
	m.listCursor = -1
	for i, r := range rows {
		if r.task != nil && r.task.GID == prev {
			m.listCursor = i
			break
		}
	}
	if m.listCursor < 0 {
		m.listCursor = m.nextTaskRow(0, 1)
	}
	m.ensureListVisible()
	t := m.selectedTask()
	if t == nil {
		m.detail = nil
		m.refreshDetailView()
		return nil
	}
	if t.GID == prev && m.detail != nil {
		return m.loadDetail(t.GID) // refresh in place
	}
	return tea.Batch(m.loadDetail(t.GID), m.debounceDetail(t.GID))
}

func (m *Model) moveList(delta int) tea.Cmd {
	if len(m.rows) == 0 {
		return nil
	}
	dir := 1
	if delta < 0 {
		dir = -1
	}
	target := clamp(m.listCursor+delta, 0, len(m.rows)-1)
	next := m.nextTaskRow(target, dir)
	if next < 0 {
		next = m.nextTaskRow(target, -dir)
	}
	if next < 0 || next == m.listCursor {
		return nil
	}
	m.listCursor = next
	m.ensureListVisible()
	t := m.selectedTask()
	return tea.Batch(m.loadDetail(t.GID), m.debounceDetail(t.GID))
}

// nextTaskRow returns the first task row at or after i in direction dir, or -1.
func (m *Model) nextTaskRow(i, dir int) int {
	for ; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].task != nil {
			return i
		}
	}
	return -1
}

func (m *Model) selectedTask() *store.TaskSummary {
	if m.listCursor < 0 || m.listCursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.listCursor].task
}

func (m *Model) debounceDetail(gid string) tea.Cmd {
	if m.syncer == nil {
		return nil
	}
	return tea.Tick(400*time.Millisecond, func(time.Time) tea.Msg { return detailDebounceMsg{gid: gid} })
}

func (m *Model) needsDetailFetch() bool {
	if m.detail == nil || m.detail.DetailsFetchedAt == "" {
		return true
	}
	t, err := time.Parse(time.RFC3339, m.detail.DetailsFetchedAt)
	return err != nil || m.deps.Now().Sub(t) > detailRefreshAge
}

func (m *Model) narrow() bool { return m.width > 0 && m.width < narrowWidth }

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	return min(max(v, lo), hi)
}

// syncStatus is shown in the header.
func (m *Model) syncStatus() (string, bool) {
	switch {
	case m.syncing:
		return "⟳ 同期中", true
	case m.deps.NewSyncer == nil:
		return "○ オフライン", false
	case m.syncErr != nil:
		if errors.Is(m.syncErr, context.Canceled) {
			return "", true
		}
		return "○ オフライン（" + shortErr(m.syncErr) + "）", false
	case !m.lastSync.IsZero():
		return "● 同期済み " + m.lastSync.Format("15:04"), true
	}
	return "", true
}

func shortErr(err error) string {
	s := err.Error()
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40]) + "…"
	}
	return s
}

