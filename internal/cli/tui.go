package cli

import (
	"context"
	"os"
	"os/exec"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/amasok/asana-tui/internal/browser"
	"github.com/amasok/asana-tui/internal/imageview"
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

	deps := tui.Deps{Store: st, SyncInterval: tuiSyncInterval, OpenURL: browser.Open, ViewImages: a.viewImages}
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

// viewImages shows images: inline in a full-screen viewer (a child process that takes over
// the terminal while the TUI is suspended), or in the OS image viewer.
func (a *app) viewImages(paths []string) tea.Cmd {
	protocol := imageview.Resolve(a.cfg.UI.ImageProtocol)
	self, err := os.Executable()
	if protocol == imageview.ProtocolOpen || err != nil {
		return func() tea.Msg {
			_ = imageview.OpenExternal(paths...)
			return nil
		}
	}
	args := append([]string{"image-view", "--protocol", protocol}, paths...)
	return tea.ExecProcess(exec.Command(self, args...), func(error) tea.Msg { return nil })
}
