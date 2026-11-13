// Package tui is the Bubble Tea terminal UI. It reads everything from the local store, so it
// starts instantly and works offline; syncing with Asana runs in the background.
package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/imageview"
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
	// ViewImages shows image files (e.g. by running the image viewer with tea.ExecProcess).
	ViewImages func(paths []string) tea.Cmd
	// InlineImages draws images inside the detail pane with kitty Unicode placeholders.
	InlineImages bool
	// RunEditor opens path in the user's editor and reports back with done
	// (production: tea.ExecProcess with $EDITOR).
	RunEditor func(path string, done func(error) tea.Msg) tea.Cmd
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
	header    string
	key       string // stable id of a group header (section gid / bucket name)
	count     int    // tasks in the group
	collapsed bool
	depth     int // 0 = task in the list, 1+ = expanded subtask
	task      *store.TaskSummary
}

// selectable rows: tasks, and collapsed group headers (so they can be expanded again).
func (r row) selectable() bool { return r.task != nil || r.collapsed }

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

	// writes (write.go)
	prompt      textinput.Model
	promptMode  promptKind
	selectAfter string // gid to select after the next list load (e.g. a created task)
	syncAgain   bool   // a write happened during a sync; sync again when it finishes
	outbox      outboxCounts
	showOutbox  bool
	ops         []store.Op
	opCursor    int

	// views (board.go)
	zoom           bool // detail pane fills the screen
	board          bool // project tasks as a board (sections = columns)
	boardCol       int
	boardRow       int
	boardColOffset int
	tagFilter      string
	picker         *picker

	// collapsible groups (collapse.go)
	allRows   []row           // every row; rows is allRows minus collapsed groups
	collapsed map[string]bool // keys of collapsed groups in the current scope
	renameGID string          // section being renamed (promptRenameSection)
	// collapsedScope is the scope whose collapsed groups are loaded into collapsed.
	collapsedScope string

	// detail rendering (markdown.go)
	dark         bool // terminal background
	mdCache      map[mdKey]string
	inline       map[string]*inlineImage // by attachment gid
	inlineDirty  bool
	nextImageID  int
	cellW, cellH int // terminal cell size in pixels (0 if unknown)

	// layout (layout.go)
	detailRatio int // detail pane width in % of the area right of the sidebar
	hideSidebar bool

	// subtasks (subtasks.go)
	expanded      map[string]bool // tasks whose subtasks are shown under them
	subtaskParent string          // parent for promptSubtask

	// detail navigation (detailnav.go)
	detailStack  []string     // tasks opened from the detail view (subtasks); top is shown
	detailCursor int          // selected item in the detail view
	detailItems  []detailItem // items of the rendered detail (fields, subtasks, add rows)
	returnTo     string       // subtask to reselect after going back to its parent
	commentGID   string       // task the comment prompt posts to

	// keys (keymap.go) and choosers (pickers.go)
	leader         bool              // space was pressed: the menu is open
	renderedActive bool              // the detail was last rendered with selectable items
	dueTask        store.TaskSummary // task for promptDue
	tagTask        store.TaskSummary // task for promptNewTag
}

func New(ctx context.Context, deps Deps) *Model {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	ti := newInput()
	ti.Prompt = "/ "
	ti.Placeholder = "タスク名・説明・コメントを検索"
	m := &Model{
		deps:        deps,
		ctx:         ctx,
		st:          newStyles(true),
		focus:       paneList,
		scopes:      []scope{{kind: scopeMyTasks, name: "My Tasks"}},
		detailView:  viewport.New(),
		search:      ti,
		prompt:      newInput(),
		dark:        true,
		detailRatio: defaultDetailRatio,
	}
	if deps.InlineImages {
		m.cellW, m.cellH = imageview.CellSize()
	}
	return m
}

// newInput returns a text input with a steady (non-blinking) cursor. Blinking adds a
// timer to every keystroke and makes the UI harder to test deterministically.
func newInput() textinput.Model {
	ti := textinput.New()
	st := ti.Styles()
	st.Cursor.Blink = false
	ti.SetStyles(st)
	return ti
}

// --- messages ---

type (
	scopesLoadedMsg struct {
		projects []store.ProjectView
		err      error
	}
	tasksLoadedMsg struct {
		seq       int
		rows      []row
		err       error
		scope     string
		collapsed map[string]bool
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
	return tea.Batch(tea.RequestBackgroundColor, m.loadLayout(), m.loadScopes(), m.loadTasks(), m.loadOutbox(), m.startSync())
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
	seq, sc, query, filter, tag := m.taskSeq, m.currentScope(), m.query, m.filter, m.tagFilter
	st, now, scopeKey := m.deps.Store, m.deps.Now(), m.scopeKey()
	needCollapsed := scopeKey != m.collapsedScope
	expanded := make(map[string]bool, len(m.expanded))
	for k, v := range m.expanded {
		expanded[k] = v
	}
	var subCompleted *bool
	switch filter {
	case filterAll:
	case filterCompleted:
		subCompleted = boolPtr(true)
	default:
		subCompleted = boolPtr(false)
	}
	return func() tea.Msg {
		rows, err := queryRows(m.ctx, st, sc, query, filter, tag, now)
		if err == nil {
			rows, err = withSubtasks(m.ctx, st, rows, expanded, subCompleted)
		}
		msg := tasksLoadedMsg{seq: seq, rows: rows, err: err, scope: scopeKey}
		if needCollapsed {
			msg.collapsed = loadCollapsed(m.ctx, st, scopeKey)
		}
		return msg
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

// Update handles a message, then keeps the detail pane's width in step with the layout and
// sends any inline images that need (re)sending.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	model, cmd := m.update(msg)
	if m.inDetail() != m.renderedActive {
		m.refreshDetailView() // items appear / disappear with the detail focus
	}
	m.syncDetailWidth()
	return model, tea.Batch(cmd, m.flushInline())
}

func (m *Model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if cmd, ok := m.updateEdit(msg); ok {
		return m, cmd
	}
	switch msg := msg.(type) {
	case inlineLoadedMsg:
		m.onInlineLoaded(msg)
		return m, nil
	case layoutLoadedMsg:
		m.applyLayout(msg)
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
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
		before := m.currentScope().sections
		m.setProjects(msg.projects)
		// Group headers use section names from the scope, so reload when they changed
		// (e.g. after a rename or a sync).
		if !slices.Equal(before, m.currentScope().sections) {
			return m, m.loadTasks()
		}
		return m, nil

	case tasksLoadedMsg:
		if msg.seq != m.taskSeq {
			return m, nil
		}
		if msg.err != nil {
			m.rows, m.allRows = nil, nil
			m.status = msg.err.Error()
			return m, nil
		}
		if msg.collapsed != nil && msg.scope == m.scopeKey() {
			m.collapsed, m.collapsedScope = msg.collapsed, msg.scope
		}
		return m, m.setRows(msg.rows)

	case detailLoadedMsg:
		if msg.gid != m.detailGID() {
			return m, nil
		}
		if msg.err != nil {
			m.detail = nil
			m.status = msg.err.Error()
		} else {
			keepScroll := m.detail != nil && m.detail.GID == msg.gid
			m.detail = msg.d
			m.placeCursorAfterLoad(keepScroll)
			m.refreshDetailView()
			m.detailCursor = clamp(m.detailCursor, 0, len(m.detailItems)-1)
			if !keepScroll {
				m.detailView.GotoTop()
			}
			m.scrollToCursor()
			return m, m.prepareInline()
		}
		return m, nil

	case detailDebounceMsg:
		if msg.gid != m.detailGID() || !m.needsDetailFetch() {
			return m, nil
		}
		return m, m.fetchDetails(msg.gid)

	case detailFetchedMsg:
		if m.fetchingFor == msg.gid {
			m.fetchingFor = ""
		}
		var attErr *sync.AttachmentsError
		if errors.As(msg.err, &attErr) {
			m.status = attErr.Error() + attachmentsHint(attErr)
		} else if msg.err != nil {
			m.status = "詳細を取得できませんでした（オフライン表示）: " + msg.err.Error()
			return m, nil
		}
		var cmds []tea.Cmd
		if m.expanded[msg.gid] {
			cmds = append(cmds, m.loadTasks()) // its subtasks may have just arrived
		}
		if msg.gid == m.detailGID() {
			cmds = append(cmds, m.loadDetail(msg.gid))
		}
		return m, tea.Batch(cmds...)

	case syncDoneMsg:
		m.syncing = false
		m.syncErr = msg.err
		if msg.syncer != nil {
			m.syncer = msg.syncer
		}
		cmds := []tea.Cmd{m.scheduleSync(), m.loadOutbox()}
		if m.syncAgain {
			m.syncAgain = false
			cmds = append(cmds, m.startSync())
		}
		if msg.err == nil {
			m.lastSync = msg.at
			cmds = append(cmds, m.loadScopes(), m.loadTasks())
			if gid := m.detailGID(); gid != "" {
				cmds = append(cmds, m.loadDetail(gid), m.debounceDetail(gid))
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
	if model, cmd, ok := m.updateWrite(msg); ok {
		return model, cmd
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

	// modal layers first: text input, overlays, the space menu
	switch {
	case m.promptMode != promptNone:
		return m.handlePromptKey(msg)
	case m.showOutbox:
		return m.handleOutboxKey(key)
	case m.picker != nil:
		return m.handlePickerKey(key)
	case m.searching:
		return m.handleSearchKey(msg)
	case m.leader:
		m.leader = false
		return m, m.runMenuKey(key)
	case m.showHelp:
		m.showHelp = false
		return m, nil
	}

	// keys that mean the same everywhere
	switch key {
	case "space":
		m.leader = true
		return m, nil
	case "?":
		m.showHelp = true
		return m, nil
	case "/":
		m.searching = true
		m.search.SetValue(m.query)
		m.search.CursorEnd()
		return m, m.search.Focus()
	case "esc", "backspace", "q":
		return m, m.back(key)
	case "tab":
		if !m.zoom {
			m.focus = (m.focus + 1) % 3
		}
		return m, nil
	case "shift+tab":
		if !m.zoom {
			m.focus = (m.focus + 2) % 3
		}
		return m, nil
	}
	if cmd, ok := m.runShortcut(key); ok {
		return m, cmd
	}

	// movement, by context
	switch m.keyContext() {
	case ctxDetail:
		return m, m.handleDetailKey(key)
	case ctxSidebar:
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
	case ctxBoard:
		return m.handleBoardKey(key)
	case ctxList:
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
		case "enter":
			if m.selectedTask() != nil {
				return m, m.openDetail()
			} else if m.listCursor >= 0 && m.listCursor < len(m.rows) && m.rows[m.listCursor].collapsed {
				return m, m.toggleGroup() // expand
			}
		case "l", "right":
			return m, m.expandOrOpen()
		case "h", "left":
			return m, m.collapseOrUp()
		}
	}
	return m, nil
}

// back handles esc / backspace / q: leave the innermost thing (a subtask opened in the
// detail, the full-screen detail, the search, the detail focus); q quits at the top.
func (m *Model) back(key string) tea.Cmd {
	if m.inDetail() {
		if cmd, ok := m.popDetail(); ok {
			return cmd
		}
	}
	switch {
	case m.zoom:
		m.zoom = false
		m.focus = paneList
		return nil
	case key != "q" && m.query != "":
		m.query = ""
		m.search.Reset()
		return m.loadTasks()
	case m.focus == paneDetail:
		m.focus = paneList
		return nil
	case key == "q" && m.narrow() && m.focus != paneSidebar:
		m.focus--
		return nil
	case key == "q":
		return tea.Quit
	}
	return nil
}

func (m *Model) handleSearchKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
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
	case "up", "ctrl+p":
		return m, m.moveList(-1) // move through results while typing
	case "down", "ctrl+n":
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
	m.boardCol, m.boardRow, m.boardColOffset = 0, 0, 0
	return m.loadTasks()
}

// setRows replaces the list, keeping the selected task if it is still there.
func (m *Model) setRows(rows []row) tea.Cmd {
	var prev string
	if t := m.selectedTask(); t != nil {
		prev = t.GID
	}
	m.allRows = rows
	m.rows = m.visibleRows()
	target := prev
	if m.selectAfter != "" && m.listed(m.selectAfter) {
		target = m.selectAfter
	}
	m.selectAfter = ""
	m.selectInList(target)
	if m.boardActive() {
		m.selectInBoard(target)
	}
	if t := m.selectedTask(); t == nil || t.GID != prev {
		m.resetDetailNav()
	}
	t := m.selectedTask()
	if t == nil {
		m.detail = nil
		m.refreshDetailView()
		return nil
	}
	if gid := m.detailGID(); gid != t.GID || (t.GID == prev && m.detail != nil) {
		return m.loadDetail(gid) // refresh in place (or the subtask opened in the detail)
	}
	return tea.Batch(m.loadDetail(t.GID), m.debounceDetail(t.GID))
}

// listed reports whether gid is in the task list (or on the board).
func (m *Model) listed(gid string) bool {
	for _, r := range m.allRows {
		if r.task != nil && r.task.GID == gid {
			return true
		}
	}
	return false
}

// selectInList puts the list cursor on gid, or on the first task if gid is not listed.
func (m *Model) selectInList(gid string) {
	m.listCursor = -1
	for i, r := range m.rows {
		if r.task != nil && r.task.GID == gid {
			m.listCursor = i
			break
		}
	}
	if m.listCursor < 0 {
		m.listCursor = m.nextTaskRow(0, 1)
	}
	m.ensureListVisible()
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
	m.resetDetailNav()
	t := m.selectedTask()
	if t == nil { // a collapsed group header
		return nil
	}
	return tea.Batch(m.loadDetail(t.GID), m.debounceDetail(t.GID))
}

// nextTaskRow returns the first task row at or after i in direction dir, or -1.
func (m *Model) nextTaskRow(i, dir int) int {
	for ; i >= 0 && i < len(m.rows); i += dir {
		if m.rows[i].selectable() {
			return i
		}
	}
	return -1
}

func (m *Model) selectedTask() *store.TaskSummary {
	if m.boardActive() {
		return m.boardSelected()
	}
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

// attachmentsHint explains the most likely fix for an attachments error.
func attachmentsHint(e *sync.AttachmentsError) string {
	if strings.Contains(e.Error(), "403") || strings.Contains(e.Error(), "scope") {
		return "（Asana のアプリ設定で attachments:read の権限を追加し、config の scopes にも足してから auth login し直してください）"
	}
	return ""
}

func shortErr(err error) string {
	s := err.Error()
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40]) + "…"
	}
	return s
}
