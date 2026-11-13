package cli

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/browser"
	"github.com/amasok/asana-tui/internal/sync"
	"github.com/amasok/asana-tui/internal/tui"
)

// tuiSyncInterval is the background sync interval (requirements 8 章: default 5 minutes).
const tuiSyncInterval = 5 * time.Minute

func (a *app) runTUI(ctx context.Context) error {
	st, err := a.openStore(ctx)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	deps := tui.Deps{Store: st, SyncInterval: tuiSyncInterval, OpenURL: browser.Open}
	if !a.offline {
		deps.NewSyncer = func(ctx context.Context) (*sync.Syncer, error) {
			api, err := a.apiClient(ctx)
			if err != nil {
				return nil, err
			}
			return &sync.Syncer{API: api, Store: st, Cfg: a.cfg.Sync}, nil
		}
	}
	m := tui.New(ctx, deps)
	_, err = tea.NewProgram(m, tea.WithContext(ctx)).Run()
	return err
}
