package tui

import (
	"errors"
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asagao/internal/store"
)

// Resetting the local data (to use another Asana account), with a confirmation that shows
// how many unsent changes would go, and an automatic backup of them (store.ResetWithBackup).

type (
	resetAskMsg struct {
		unsent int
		newest int64 // the newest unsent change: the reset deletes none queued after it
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
		n, newest, err := st.UnsentState(m.ctx)
		return resetAskMsg{unsent: n, newest: newest, err: err}
	}
}

func (m *Model) showResetConfirm(unsent int, newest int64) {
	title := "ローカルのデータを初期化しますか？（ログイン情報は残り、次の同期でログイン中のアカウントのデータを取り込みます）"
	confirm := "初期化する"
	if unsent > 0 {
		title = fmt.Sprintf("ローカルのデータを初期化しますか？ Asana に送っていない変更 %d 件も消えます。"+
			"消した変更の内容は、破棄した変更の一覧（%s）から確認できます（JSON ファイルにも自動でバックアップします）", unsent, m.actionKey("outbox"))
		confirm = fmt.Sprintf("未送信の %d 件ごと初期化する", unsent)
	}
	m.picker = &picker{kind: pickReset, title: title,
		items: []pickerItem{{label: "やめる"}, {label: confirm, value: strconv.FormatInt(newest, 10)}}}
}

// reset runs after the confirmation; value is the newest unsent change the user agreed to
// lose ("" = cancelled). If a change was queued after it, the store deletes nothing and
// we ask again with the new count.
func (m *Model) reset(value string) tea.Cmd {
	if value == "" {
		return nil
	}
	upTo, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return nil
	}
	st, dir, now := m.deps.Store, m.deps.BackupDir, m.deps.Now()
	return func() tea.Msg {
		res, err := st.ResetWithBackup(m.ctx, dir, upTo, now)
		return resetDoneMsg{res: res, err: err}
	}
}

func (m *Model) updateReset(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case resetAskMsg:
		if msg.err != nil {
			m.status = "エラー: " + msg.err.Error()
		} else {
			m.showResetConfirm(msg.unsent, msg.newest)
		}
		return nil, true
	case resetDoneMsg:
		switch {
		case errors.Is(msg.err, store.ErrUnsentChanges):
			m.status = "未送信の変更が増えたため、何も消さずに初期化をやめました。もう一度確かめてください"
			return m.startReset(), true
		case errors.Is(msg.err, store.ErrPushBusy):
			m.status = "別の asagao が同期・送信中のため初期化できません。終わってからやり直してください"
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
