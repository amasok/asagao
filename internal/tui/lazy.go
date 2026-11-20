package tui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// Projects whose tasks are fetched when they are opened (#54). The sidebar marks a project
// whose tasks are not cached (unfetchedMark). Once the cursor rests on one, it is recorded
// as a project to sync and, online, its incomplete tasks are fetched, then its completed
// ones. Offline (or while another sync runs), the next sync fetches it.

// unfetchedMark marks a project in the sidebar whose tasks are not cached yet.
const unfetchedMark = "◌ "

// openDelay is how long the cursor must rest on a project before it is fetched: moving
// through the list fetches nothing.
const openDelay = 400 * time.Millisecond

// unfetchedNote explains, at the top of a project's list, why its tasks are missing.
const unfetchedNote = "◌ このプロジェクトのタスクはまだ取得していません。開くと取得します（オフラインのときは次の同期で）"

type (
	projectOpenTickMsg struct{ gid string }
	projectOpenedMsg   struct {
		gid       string
		completed bool // the completed tasks were asked for
		res       sync.OpenResult
		err       error
	}
	completedFetchedMsg struct {
		res sync.Result
		err error
	}
)

// openProjectLater asks to fetch the current project, if its tasks are not cached, once the
// cursor rested on it for openDelay.
func (m *Model) openProjectLater() tea.Cmd {
	sc := m.currentScope()
	if sc.kind != scopeProject || sc.fetched {
		return nil
	}
	gid := sc.project
	return tea.Tick(openDelay, func(time.Time) tea.Msg { return projectOpenTickMsg{gid: gid} })
}

// openProject records the project as one to sync and fetches its tasks (completed: the
// completed ones too, after the incomplete ones were shown).
func (m *Model) openProject(gid string, completed bool) tea.Cmd {
	m.opening = gid
	st, syncer := m.deps.Store, m.syncer
	if m.syncState.running || syncer == nil {
		// The sync running (or the next one, once online) fetches it.
		syncing := m.syncState.running
		if syncing {
			m.syncState.again = true
		}
		return func() tea.Msg {
			_, err := st.WantProject(m.ctx, gid)
			return projectOpenedMsg{gid: gid, err: err, res: sync.OpenResult{}}
		}
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 2*time.Minute)
		defer cancel()
		res, err := syncer.OpenProject(ctx, gid, completed)
		return projectOpenedMsg{gid: gid, completed: completed, res: res, err: err}
	}
}

func (m *Model) updateLazy(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case projectOpenTickMsg:
		sc := m.currentScope()
		if sc.kind != scopeProject || sc.project != msg.gid || sc.fetched || m.opening == msg.gid {
			return m, nil, true
		}
		return m, m.openProject(msg.gid, false), true

	case projectOpenedMsg:
		if m.opening == msg.gid {
			m.opening = ""
		}
		switch {
		case errors.Is(msg.err, sync.ErrPushBusy):
			// Another asagao is syncing: the project is recorded, a sync soon fetches it.
			return m, m.retrySyncAfter(busyRetryMin), true
		case msg.err != nil:
			m.status = "プロジェクトのタスクを取得できませんでした（次の同期で取得します）: " + msg.err.Error()
			return m, nil, true
		}
		cmds := []tea.Cmd{m.loadScopes(), m.loadTasks()}
		if msg.res.State == readmodel.TasksOpen && !msg.completed && m.syncer != nil {
			cmds = append(cmds, m.openProject(msg.gid, true)) // the incomplete ones are shown first
		}
		return m, tea.Batch(cmds...), true

	case completedFetchedMsg:
		m.syncState.running = false
		if msg.err != nil && !errors.Is(msg.err, sync.ErrPushBusy) {
			m.syncState.err = msg.err // the next sync fetches them
		}
		cmds := []tea.Cmd{m.loadScopes(), m.loadTasks()}
		if m.syncState.again {
			m.syncState.again = false
			cmds = append(cmds, m.startSync())
		}
		return m, tea.Batch(cmds...), true
	}
	return m, nil, false
}

// fetchCompleted fetches the completed tasks a sync left for later (Result.CompletedPending),
// as part of the sync: no other sync starts meanwhile.
func (m *Model) fetchCompleted() tea.Cmd {
	syncer := m.syncer
	if syncer == nil {
		return nil
	}
	m.syncState.running = true
	return func() tea.Msg {
		res, err := syncer.FetchCompleted(m.ctx)
		return completedFetchedMsg{res: res, err: err}
	}
}
