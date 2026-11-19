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

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/imageview"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// Deps are the TUI's external dependencies (swapped out in tests).
type Deps struct {
	Store Store
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
	// Theme forces "dark" or "light" colors ("" / "auto": follow the terminal background).
	Theme string
	// Accent overrides the accent color (e.g. "#7aa2f7").
	Accent string
	// KeyBindings rebinds action shortcuts: action id -> keys (see ActionIDs).
	KeyBindings map[string][]string
	// CacheDir is where files to upload are copied (so they can be sent later).
	CacheDir string
	// BackupDir is where unsent changes are written before the local data is reset.
	BackupDir string
	// RunEditor opens path in the user's editor and reports back with done
	// (production: tea.ExecProcess with $EDITOR).
	RunEditor func(path string, done func(error) tea.Msg) tea.Cmd
	// AuthScopes is config auth.scopes (to explain how to enable the inbox).
	AuthScopes []string
	// Searches is searches.toml, the saved searches (searches.go; nil: none).
	Searches SearchFile
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
	scopeRecent // tasks modified in the last 7 days (when the inbox cannot be fetched)
	scopeInbox  // Asana notifications (inbox.go)
)

type scope struct {
	kind     scopeKind
	name     string
	project  string // gid
	sections []readmodel.SectionView
	status   string // latest status update type of the project
	group    string // the sidebar heading it is under (readmodel.ProjectGroup)
	notice   string // scopeRecent: why the inbox is not shown (inbox.go)
	fetched  bool   // scopeProject: its tasks are cached (lazy.go)
}

// row is one line of the task list: a group header or a task.
type row struct {
	header    string
	key       string // stable id of a group header (section gid / bucket name)
	count     int    // tasks in the group
	collapsed bool
	depth     int // 0 = task in the list, 1+ = expanded subtask
	task      *readmodel.TaskSummary
	notif     *readmodel.Notification // inbox: the notification (task is what it is about, if a task)
	note      bool                    // header is an explanation, not a group (never collapsed)
	empty     bool                    // header of a section with no tasks (shown so it can be changed)
}

// selectable rows: tasks, notifications, collapsed group headers (so they can be expanded
// again) and empty sections (so they can be renamed, moved or deleted).
func (r row) selectable() bool {
	return r.task != nil || r.notif != nil || r.collapsed || r.empty
}

// isHeader reports whether r is a group header (or a note).
func (r row) isHeader() bool { return r.task == nil && r.notif == nil }

// id identifies a row for keeping the selection across reloads: an inbox row by its
// notification (several can be about one task), others by their task.
func (r row) id() string {
	switch {
	case r.notif != nil:
		return "notification:" + r.notif.GID
	case r.task != nil:
		return r.task.GID
	}
	return ""
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

	detail     *readmodel.TaskDetail
	detailMe   string // the logged-in user when detail was loaded: their comments can be changed
	detailView viewport.Model

	search    textinput.Model
	searching bool // input has focus
	query     string
	remote    remoteFound // what Asana found for a search (remotesearch.go)
	filter    filterMode

	syncer      *sync.Syncer
	syncing     bool
	syncErr     error
	syncBusy    bool // the last sync found another process syncing; retrying soon
	busyRetries int  // busy syncs in a row (the retry waits longer each time)
	lastSync    time.Time
	fetchingFor string // gid whose details are being fetched
	opening     string // project whose tasks are being fetched (lazy.go)

	status   string // transient message for the footer
	showHelp bool

	// writes (write.go)
	prompt      textinput.Model
	promptMode  promptKind
	selectAfter string // gid to select after the next list load (e.g. a created task)
	syncAgain   bool   // a write happened during a sync; sync again when it finishes
	outbox      outboxCounts
	showOutbox  bool
	ops         []outbox.Op
	opCursor    int
	inbox       readmodel.InboxState // whether the notifications can be shown (inbox.go)
	inboxUnread int
	discards    []readmodel.Discard // recently dropped changes, newest first (shown in full in the outbox)
	outboxTop   int                 // first line of the outbox overlay on screen (it scrolls)

	// views (board.go)
	zoom           bool // detail pane fills the screen
	board          bool // project tasks as a board (sections = columns)
	boardCol       int
	boardRow       int
	boardColOffset int
	tagFilter      string
	picker         *picker
	saved          *readmodel.SavedSearch // the saved search shown (searches.go)

	// collapsible groups (collapse.go)
	allRows   []row           // every row; rows is allRows minus collapsed groups
	collapsed map[string]bool // keys of collapsed groups in the current scope
	renameGID string          // section being renamed (promptRenameSection)
	// sections and tags (sectiontags.go)
	planOff         map[string]planWindow // features the workspace's plan lacks (402), and until when
	planSeq         uint64                // latest loadPlan in flight
	newSectionAfter string                // promptNewSection: the section to add it after
	tagList         []readmodel.TagCount // the tags the tag manager shows
	tagTarget       readmodel.TagCount   // the tag being changed
	// time tracking (timetracking.go): the task whose entries are shown, the entry edited
	timeTask  readmodel.TaskSummary
	timeEntry string
	// collapsedScope is the scope whose collapsed groups are loaded into collapsed.
	collapsedScope string
	// rowsScope is the scope whose tasks are in allRows (to tell a reload from a switch).
	rowsScope string

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

	// calendar (calendar.go)
	calendar bool
	calDay   time.Time // selected day
	calMonth time.Time // month of calTasks
	calTasks map[string][]readmodel.TaskSummary

	// timeline (timeline.go)
	timeline bool
	tlStart  time.Time // first day shown (zero: a week before today)
	tlDayW   int       // columns per day (0: the default)
	tlFollow string    // the task the timeline last scrolled to

	// projects (projects.go)
	statusView         bool
	statusOffset       int
	statuses           []readmodel.StatusUpdateView
	selectProjectAfter string
	// the project overview (project_overview.go)
	overviewView   bool
	overviewOffset int
	overview       *readmodel.ProjectOverview
	newProjectName string // pickTeam: the project to create
	// goals and portfolios (goals.go)
	goals goalScreen

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
	leader         bool                    // space was pressed: the menu is open
	menuPage       int                     // the page of the menu shown, when it is too high for the screen
	renderedActive bool                    // the detail was last rendered with selectable items
	dueTask        readmodel.TaskSummary   // task for promptDue and promptStart
	startOffered   bool                    // the detail's task can be given a start date (startdate.go)
	bulkDueTasks   []readmodel.TaskSummary // promptDue for the marked tasks (bulk.go)
	marked         map[string]bool         // gids of the tasks marked in the list (bulk.go)
	bulkAsk        *bulkPlan               // a change to the marked tasks waiting for y/n
	tagTask        readmodel.TaskSummary   // task for promptNewTag
	// custom fields (fields.go)
	fieldTask   readmodel.TaskSummary     // task for promptField
	fieldEdit   readmodel.CustomFieldView // field for promptField
	fieldFilter fieldFilter
	fieldSaving map[string]bool         // fieldKey: a multi_enum toggle of it is being saved
	fieldQueued map[string]*queuedField // fieldKey: its latest toggle made meanwhile
	// dependencies (deps.go)
	depTask       readmodel.TaskSummary // task for promptDepSearch
	depDependents bool
	// extras.go
	dupSource      string        // task to duplicate (promptDuplicate)
	attachTask     string        // task to attach a file to (promptAttach)
	sectionProject string        // project whose sections to choose from (detail project line)
	tmpl           templateState // templates.go
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
		st:          newStyles(true, deps.Accent),
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
	if deps.Theme == "light" {
		m.dark = false
	}
	m.st = newStyles(m.dark, deps.Accent)
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
		projects []readmodel.ProjectView
		inbox    readmodel.InboxState
		unread   int
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
		d   *readmodel.TaskDetail
		me  string // the logged-in user ("" before the first sync)
		// startOffered: the start date can be set (no recent 402 for it in the workspace)
		startOffered bool
		err          error
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
		ps, err := queryOf(st).ListProjects(m.ctx, true)
		msg := scopesLoadedMsg{projects: ps, err: err}
		if err == nil {
			msg.inbox, msg.err = queryOf(st).InboxState(m.ctx)
		}
		if msg.err == nil {
			msg.unread, msg.err = queryOf(st).UnreadNotifications(m.ctx)
		}
		return msg
	}
}

func (m *Model) loadTasks() tea.Cmd {
	return tea.Batch(m.loadList(), m.loadCalendar())
}

func (m *Model) loadList() tea.Cmd {
	m.taskSeq++
	seq, sc, query, filter, tag, field, saved := m.taskSeq, m.currentScope(), m.query, m.filter, m.tagFilter, m.fieldFilter, m.saved
	found := m.remote.foundFor(query)
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
		var rows []row
		var err error
		if saved != nil {
			rows, err = savedRows(m.ctx, st, *saved, query, filter, tag, field, now)
		} else {
			rows, err = queryRows(m.ctx, st, sc, query, filter, tag, field, now, found)
		}
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
		d, err := queryOf(st).GetTask(m.ctx, gid)
		me, _ := queryOf(st).MeGID(m.ctx) // whose comments can be changed
		offered := err == nil && app.StartDateOffered(m.ctx, st, gid, m.deps.Now())
		return detailLoadedMsg{gid: gid, d: d, me: me, startOffered: offered, err: err}
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
			s.DeferCompleted = true // the incomplete tasks are shown first (fetchCompleted)
			syncer = s
		}
		res, err := syncer.Run(m.ctx, false)
		return syncDoneMsg{syncer: syncer, res: res, err: err, at: now()}
	}
}

// After another process was found syncing, a sync is tried again after busyRetryMin,
// doubling each time up to busyRetryMax (each try asks Asana who is logged in).
const (
	busyRetryMin = 5 * time.Second
	busyRetryMax = time.Minute
)

// busyRetryDelay is the wait before the next try after n busy syncs in a row.
func busyRetryDelay(n int) time.Duration {
	d := busyRetryMin
	for i := 0; i < n && d < busyRetryMax; i++ {
		d *= 2
	}
	return min(d, busyRetryMax)
}

func (m *Model) retrySyncAfter(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return syncTickMsg{} })
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
	if cmd, ok := m.updateOverview(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateBrief(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateGoals(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateProjects(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateTemplates(msg); ok {
		return m, cmd
	}
	if cmd, ok := m.updateSearches(msg); ok {
		return m, cmd
	}
	switch msg := msg.(type) {
	case inlineLoadedMsg:
		m.onInlineLoaded(msg)
		return m, nil
	case calendarLoadedMsg:
		m.onCalendarLoaded(msg)
		return m, nil
	case layoutLoadedMsg:
		m.applyLayout(msg)
		return m, nil
	case tea.BackgroundColorMsg:
		if m.deps.Theme == "dark" || m.deps.Theme == "light" {
			return m, nil // fixed by config
		}
		m.dark = msg.IsDark()
		m.st = newStyles(m.dark, m.deps.Accent)
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
		wasInbox := m.currentScope().kind
		m.inbox, m.inboxUnread = msg.inbox, msg.unread
		m.setProjects(msg.projects)
		if wasInbox != m.currentScope().kind { // the inbox became (un)available
			return m, m.loadTasks()
		}
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
			m.pruneMarks()
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
			if errors.Is(msg.err, readmodel.ErrNotFound) && m.selectedNotification() != nil {
				m.status = "通知のタスクはまだ取得していません（オンラインなら自動で取得します）"
			}
		} else {
			keepScroll := m.detail != nil && m.detail.GID == msg.gid
			m.detail, m.detailMe, m.startOffered = msg.d, msg.me, msg.startOffered
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

	case remoteSearchMsg:
		return m, m.remoteSearchDone(msg)
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
		if msg.syncer != nil {
			m.syncer = msg.syncer
		}
		// After a login problem or another account, build the syncer again next time, so a
		// login made meanwhile (asagao auth login in another terminal) is picked up.
		loginHelp := auth.LoginHelp(msg.err)
		if loginHelp != "" || errors.Is(msg.err, sync.ErrAccountChanged) {
			m.syncer = nil
		}
		if errors.Is(msg.err, auth.ErrLoginReplaced) {
			m.syncAgain = true // check the new login's account now, not in 5 minutes
		}
		// Another asagao (CLI, MCP server, another TUI) is syncing or sending: not an
		// error and not offline. Keep the last state and quietly try again soon.
		m.syncBusy = errors.Is(msg.err, sync.ErrPushBusy)
		if m.syncBusy {
			m.syncAgain = false // the retry covers a sync requested meanwhile
			delay := busyRetryDelay(m.busyRetries)
			m.busyRetries++
			return m, tea.Batch(m.retrySyncAfter(delay), m.loadOutbox())
		}
		m.busyRetries = 0
		m.syncErr = msg.err
		if errors.Is(msg.err, sync.ErrAccountChanged) {
			m.status = m.accountChangedStatus()
		} else if loginHelp != "" {
			m.status = loginHelp
		}
		cmds := []tea.Cmd{m.scheduleSync(), m.loadOutbox()}
		if msg.err == nil && msg.res.CompletedPending {
			cmds = append(cmds, m.fetchCompleted()) // syncs again (syncAgain) after it
		} else if m.syncAgain {
			m.syncAgain = false
			cmds = append(cmds, m.startSync())
		}
		if msg.err == nil {
			m.lastSync = msg.at
			cmds = append(cmds, m.loadScopes(), m.loadTasks(), m.prefetchTemplates())
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
	if model, cmd, ok := m.updateLazy(msg); ok {
		return model, cmd
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
	case m.bulkAsk != nil:
		return m.handleBulkKey(key)
	case m.promptMode != promptNone:
		return m.handlePromptKey(msg)
	case m.showOutbox:
		return m.handleOutboxKey(key)
	case m.statusView:
		return m.handleStatusKey(key)
	case m.overviewView:
		return m.handleOverviewKey(key)
	case m.goals.open:
		return m.handleGoalKey(key)
	case m.picker != nil:
		return m.handlePickerKey(key)
	case m.searching:
		return m.handleSearchKey(msg)
	case m.leader:
		switch {
		case slices.Contains(menuNext, key):
			m.turnMenuPage(+1)
			return m, nil
		case slices.Contains(menuPrev, key):
			m.turnMenuPage(-1)
			return m, nil
		}
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
		m.menuPage = 0
		return m, tea.Batch(m.loadPlan(), m.prefetchTemplates()) // recheck 402 marks (#68) and templates when opening the menu
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
	case "tab", "shift+tab":
		switch {
		case m.zoom:
		case m.timelineActive(): // no detail pane next to the timeline
			if m.focus == paneSidebar {
				m.focus = paneList
			} else {
				m.focus = paneSidebar
			}
		case key == "tab":
			m.focus = (m.focus + 1) % 3
		default:
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
	case ctxCalendar:
		return m.handleCalendarKey(key)
	case ctxList:
		if m.timelineActive() {
			if cmd, ok := m.handleTimelineKey(key); ok {
				return m, cmd
			}
		}
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
			if m.selectedNotification() != nil {
				return m, m.openNotification()
			}
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
	if key == "esc" && m.clearMarks() {
		return nil
	}
	if m.inDetail() {
		if cmd, ok := m.popDetail(); ok {
			return cmd
		}
	}
	switch {
	case m.zoom:
		m.zoom = false
		m.focus = paneList
		if t := m.selectedTask(); t == nil || len(m.detailStack) > 0 {
			m.detailStack = nil // opened from the calendar or a subtask: back to the list's task
			if t != nil {
				return m.loadDetail(t.GID)
			}
		}
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

func (m *Model) setProjects(ps []readmodel.ProjectView) {
	current := m.currentScope()
	scopes := []scope{{kind: scopeMyTasks, name: "My Tasks"}, m.inboxScope()}
	for _, p := range ps {
		scopes = append(scopes, scope{kind: scopeProject, name: p.Name, project: p.GID, sections: p.Sections, status: p.Status,
			group: readmodel.ProjectGroup(p), fetched: !p.Unfetched})
	}
	m.scopes = scopes
	m.scopeCursor = 0
	inboxSlot := func(k scopeKind) bool { return k == scopeInbox || k == scopeRecent }
	for i, s := range scopes {
		if (s.kind == current.kind || inboxSlot(s.kind) && inboxSlot(current.kind)) && s.project == current.project {
			m.scopeCursor = i
		}
	}
	if m.selectProjectAfter != "" { // a project just created
		for i, s := range scopes {
			if s.project == m.selectProjectAfter {
				m.scopeCursor = i
				m.selectProjectAfter = ""
			}
		}
	}
}

func (m *Model) moveScope(delta int) tea.Cmd {
	next := clamp(m.scopeCursor+delta, 0, len(m.scopes)-1)
	if next == m.scopeCursor {
		return nil
	}
	m.scopeCursor = next
	m.saved = nil // the sidebar leaves a saved search
	m.listCursor, m.listOffset = 0, 0
	m.boardCol, m.boardRow, m.boardColOffset = 0, 0, 0
	return tea.Batch(m.loadTasks(), m.openProjectLater())
}

// setRows replaces the list, keeping the selected task if it is still there.
func (m *Model) setRows(rows []row) tea.Cmd {
	var prev string
	if t := m.selectedTask(); t != nil {
		prev = t.GID
	}
	prevRow := prev
	if !m.boardActive() && m.listCursor >= 0 && m.listCursor < len(m.rows) && m.rows[m.listCursor].notif != nil {
		prevRow = m.rows[m.listCursor].id()
	}
	// When a reload of the same scope drops the selected task (completed, moved away), keep
	// the cursor where it was instead of going back to the top.
	near := 0
	if key := m.scopeKey(); m.rowsScope == key {
		near = m.listCursor
	} else {
		m.rowsScope = key
	}
	m.allRows = rows
	m.showRows()
	target := prevRow
	if m.selectAfter != "" && m.listed(m.selectAfter) {
		target = m.selectAfter
	}
	m.selectAfter = ""
	m.selectInListNear(target, near)
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
func (m *Model) selectInList(gid string) { m.selectInListNear(gid, 0) }

// selectInListNear puts the list cursor on gid, or, if gid is not listed, on the first task
// at or after row near (the last task before it when there is none after).
func (m *Model) selectInListNear(gid string, near int) {
	m.listCursor = -1
	for i, r := range m.rows {
		if gid != "" && r.id() == gid {
			m.listCursor = i
			break
		}
	}
	for i, r := range m.rows {
		if m.listCursor < 0 && r.task != nil && r.task.GID == gid {
			m.listCursor = i
		}
	}
	if m.listCursor < 0 && len(m.rows) > 0 {
		near = clamp(near, 0, len(m.rows)-1)
		if m.listCursor = m.nextTaskRow(near, 1); m.listCursor < 0 {
			m.listCursor = m.nextTaskRow(near, -1)
		}
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

func (m *Model) selectedTask() *readmodel.TaskSummary {
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
	case m.syncBusy:
		return "⟳ 別の asagao が同期中（少し後にもう一度同期します）", true
	case errors.Is(m.syncErr, sync.ErrAccountChanged):
		return "○ アカウントが違うため同期を停止中", false
	case m.deps.NewSyncer == nil:
		return "○ オフライン", false
	case errors.Is(m.syncErr, auth.ErrNotLoggedIn):
		return "○ 未ログイン（asagao auth login --pat）", false
	case auth.LoginHelp(m.syncErr) != "":
		return "○ ログインし直してください（案内は下の行）", false
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
