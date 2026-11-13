// Package cli wires the cobra commands.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/auth"
	"github.com/amasok/asana-tui/internal/auth/tokenstore"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

var version = "dev"

type app struct {
	cfg     config.Config
	json    bool
	offline bool
}

func NewRoot() *cobra.Command {
	a := &app{}
	root := &cobra.Command{
		Use:           "asana-tui",
		Short:         "Asana をターミナルから操作する TUI / CLI / MCP サーバー（引数なしで TUI を起動）",
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return a.runTUI(cmd.Context()) },
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			a.cfg = cfg
			return nil
		},
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "結果を JSON で出力する")
	root.Flags().BoolVar(&a.offline, "offline", false, "Asana と同期せず、ローカルストアだけで TUI を起動する")
	root.AddCommand(a.authCmd(), a.syncCmd(), a.taskCmd(), a.searchCmd(), a.projectCmd(), a.commentCmd(), a.outboxCmd(), a.mcpCmd(), a.tagCmd())
	return root
}

func Execute() {
	if err := NewRoot().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func (a *app) tokenStore() (tokenstore.Store, error) { return tokenstore.New(a.cfg.Auth) }

func (a *app) apiClient(ctx context.Context) (*asana.Client, error) {
	ts, err := a.tokenStore()
	if err != nil {
		return nil, err
	}
	src, err := auth.TokenSource(ctx, a.cfg.Auth, ts)
	if err != nil {
		return nil, err
	}
	return asana.NewClient(oauth2.NewClient(ctx, src)), nil
}

func (a *app) openStore(ctx context.Context) (*store.Store, error) {
	return store.Open(ctx, filepath.Join(config.DataDir(), "store.db"))
}

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
