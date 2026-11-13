package tui

import (
	"errors"
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/store"
)

// Resetting the local data (to use another Asana account), with a confirmation that shows
// how many unsent changes would go, and an automatic backup of them (store.ResetWithBackup).

type (
	resetAskMsg struct {
		unsent int
		err    error
	}
	resetDoneMsg struct {
		res store.ResetResult
		err error
	}
)

func (m *Model) startReset() tea.Cmd {
	st := m.deps.Store
	return func() tea.Msg {
		n, err := st.UnsentCount(m.ctx)
		return resetAskMsg{unsent: n, err: err}
	}
}

func (m *Model) showResetConfirm(unsent int) {
	title := "ローカルのデータを初期化しますか？（ログイン情報は残り、次の同期でログイン中のアカウントのデータを取り込みます）"
	confirm := "初期化する"
	if unsent > 0 {
		title = fmt.Sprintf("ローカルのデータを初期化しますか？ Asana に送っていない変更 %d 件も消えます（消す前に JSON ファイルに自動でバックアップします）", unsent)
		confirm = fmt.Sprintf("未送信の %d 件ごと初期化する", unsent)
	}
	m.picker = &picker{kind: pickReset, title: title,
		items: []pickerItem{{label: "やめる"}, {label: confirm, value: strconv.Itoa(unsent)}}}
}

// reset runs after the confirmation; value is the number of unsent changes the user agreed
// to lose ("" = cancelled). If more appeared since, the store refuses and we ask again.
func (m *Model) reset(value string) tea.Cmd {
	if value == "" {
		return nil
	}
	agreed, _ := strconv.Atoi(value)
	st, dir, now := m.deps.Store, m.deps.BackupDir, m.deps.Now()
	return func() tea.Msg {
		res, err := st.ResetWithBackup(m.ctx, dir, agreed > 0, now)
		return resetDoneMsg{res: res, err: err}
	}
}

func (m *Model) updateReset(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case resetAskMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
		} else {
			m.showResetConfirm(msg.unsent)
		}
		return nil, true
	case resetDoneMsg:
		switch {
		case errors.Is(msg.err, store.ErrUnsentChanges):
			m.status = "未送信の変更が増えたため、初期化をやめました。もう一度確かめてください"
			m.showResetConfirm(msg.res.Unsent)
			return nil, true
		case errors.Is(msg.err, store.ErrPushBusy):
			m.status = "別の asana-tui が同期・送信中のため初期化できません。終わってからやり直してください"
			return nil, true
		case msg.err != nil:
			m.status = "エラー: " + msg.err.Error()
			return nil, true
		}
		m.status = "ローカルのデータを初期化しました。ログイン中のアカウントで同期します"
		if msg.res.BackupPath != "" {
			m.status += fmt.Sprintf("（未送信の変更 %d 件と破棄の記録のバックアップ: %s）", msg.res.Unsent, msg.res.BackupPath)
		}
		// Show the empty store; tasks are loaded again when the sync (as the logged-in
		// account) is done. Loading them now would only report that no account is set yet.
		m.syncErr, m.ops, m.discards, m.opCursor, m.scopeCursor = nil, nil, nil, 0, 0
		m.rows, m.allRows, m.detail, m.taskSeq = nil, nil, nil, m.taskSeq+1
		return tea.Batch(m.loadScopes(), m.loadOutbox(), m.requestSync()), true
	}
	return nil, false
}

// accountChangedStatus explains a sync stopped by sync.ErrAccountChanged.
func (m *Model) accountChangedStatus() string {
	unsent := m.outbox.pending + m.outbox.conflict + m.outbox.failed
	return store.AccountChangedHelp(unsent, m.actionKey("reset")+" で初期化")
}
