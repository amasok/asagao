package tui

import (
	"encoding/json"
	"slices"

	tea "charm.land/bubbletea/v2"
)

// The detail pane width (< / >) and the sidebar visibility (b) are remembered in the store.

const defaultDetailRatio = 40

var detailRatios = []int{30, 40, 50, 60, 70, 80}

type layoutState struct {
	DetailRatio int  `json:"detail_ratio"`
	HideSidebar bool `json:"hide_sidebar"`
}

type layoutLoadedMsg layoutState

func (m *Model) loadLayout() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		var l layoutState
		if v, err := st.LayoutState(m.ctx); err == nil && v != "" {
			_ = json.Unmarshal([]byte(v), &l)
		}
		return layoutLoadedMsg(l)
	}
}

func (m *Model) applyLayout(l layoutLoadedMsg) {
	if slices.Contains(detailRatios, l.DetailRatio) {
		m.detailRatio = l.DetailRatio
	}
	m.hideSidebar = l.HideSidebar
}

func (m *Model) saveLayout() tea.Cmd {
	b, _ := json.Marshal(layoutState{DetailRatio: m.detailRatio, HideSidebar: m.hideSidebar})
	st := m.deps.Store
	return func() tea.Msg {
		_ = st.SetLayoutState(m.ctx, string(b))
		return nil
	}
}

func (m *Model) layoutKey(key string) tea.Cmd {
	i := slices.Index(detailRatios, m.detailRatio)
	switch key {
	case ">":
		if i < len(detailRatios)-1 {
			m.detailRatio = detailRatios[i+1]
		}
	case "<":
		if i > 0 {
			m.detailRatio = detailRatios[i-1]
		}
	case "b":
		m.hideSidebar = !m.hideSidebar
		if m.hideSidebar && m.focus == paneSidebar {
			m.focus = paneList
		}
	}
	m.status = ""
	return m.saveLayout()
}

// syncDetailWidth resizes the detail viewport when the layout changed (and re-wraps it).
func (m *Model) syncDetailWidth() {
	if m.width == 0 || ((m.boardActive() || m.timelineActive()) && !m.zoom) {
		return
	}
	_, _, dw := m.paneWidths()
	if dw == 0 {
		return
	}
	if m.detail.view.Width() != dw-4 || m.detail.view.Height() != m.listHeight() {
		m.detail.view.SetWidth(max(1, dw-4))
		m.detail.view.SetHeight(m.listHeight())
		m.refreshDetailView()
	}
}
