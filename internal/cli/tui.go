package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/browser"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/imageview"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/tui"
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

	keys, err := a.cfg.KeyBindings()
	if err != nil {
		return err
	}
	for id := range keys {
		if !slices.Contains(tui.ActionIDs(), id) {
			return fmt.Errorf("config [keys]: unknown action %q (see `asagao keys`)", id)
		}
	}
	deps := tui.Deps{Store: st, SyncInterval: tuiSyncInterval, OpenURL: browser.Open, ViewImages: a.viewImages,
		InlineImages: a.inlineImages(), RunEditor: a.runEditor,
		Theme: a.cfg.UI.Theme, Accent: a.cfg.UI.Accent, KeyBindings: keys,
		CacheDir: filepath.Join(config.CacheDir(), "attachments"), BackupDir: backupDir(), AuthScopes: a.cfg.Auth.Scopes}
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

// inlineImages reports whether to draw images inside the detail pane (kitty Unicode
// placeholders; supported by kitty and Ghostty, not through multiplexers).
func (a *app) inlineImages() bool {
	return inlineImagesEnabled(a.cfg.UI.InlineImages, a.cfg.UI.ImageProtocol, os.Getenv)
}

// inlineImagesEnabled decides ui.inline_images. "auto" turns it on only when the terminal
// is known to draw Unicode placeholders (kitty, Ghostty, herdr): ui.image_protocol = "kitty"
// is not enough, as tmux and zellij pass kitty images through but leave placeholders blank.
// Otherwise images are shown as "[画像N]（i で表示）"; inline_images = "on" forces it.
func inlineImagesEnabled(setting, protocol string, getenv func(string) string) bool {
	switch setting {
	case "on":
		return true
	case "off":
		return false
	}
	if protocol != "" && protocol != imageview.ProtocolAuto && protocol != imageview.ProtocolKitty {
		return false // iterm / open chosen explicitly
	}
	return imageview.PlaceholdersSupported(getenv)
}

// runEditor opens path in ui.editor, $VISUAL, $EDITOR, nvim or vi (in that order) while the
// TUI is suspended.
func (a *app) runEditor(path string, done func(error) tea.Msg) tea.Cmd {
	cmdline := a.cfg.UI.Editor
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if cmdline == "" {
			cmdline = os.Getenv(env)
		}
	}
	if cmdline == "" {
		cmdline = "vi"
		if _, err := exec.LookPath("nvim"); err == nil {
			cmdline = "nvim"
		}
	}
	args := strings.Fields(cmdline)
	return tea.ExecProcess(exec.Command(args[0], append(args[1:], path)...), done)
}

func (a *app) keysCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "keys",
		Short: "TUI の操作名の一覧（config.toml の [keys] でキーを変えるときに使う）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), "config.toml の例:\n\n[keys]\ncomplete = \"d\"\ndue = [\"D\", \"ctrl+t\"]\n\n使える操作名:")
			for _, id := range tui.ActionIDs() {
				fmt.Fprintln(cmd.OutOrStdout(), "  "+id)
			}
			return nil
		},
	}
}
