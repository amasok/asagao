package cli

import (
	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/imageview"
)

// imageViewCmd is run by the TUI (via tea.ExecProcess) to show images full screen.
func (a *app) imageViewCmd() *cobra.Command {
	var protocol string
	cmd := &cobra.Command{
		Use:    "image-view <file>...",
		Short:  "画像をターミナルに全画面表示する（TUI から使う）",
		Hidden: true,
		Args:   cobra.MinimumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if protocol == "" {
				protocol = a.cfg.UI.ImageProtocol
			}
			return imageview.Run(args, protocol)
		},
	}
	cmd.Flags().StringVar(&protocol, "protocol", "", "auto / iterm / kitty / open")
	return cmd
}
