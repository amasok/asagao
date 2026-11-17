// Package cli wires the cobra commands.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"golang.org/x/oauth2"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/auth/tokenstore"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/termsafe"
)

var version = "dev"

type app struct {
	cfg     config.Config
	json    bool
	offline bool
	// baseURL overrides the Asana API URL (tests).
	baseURL string
	// jsonOut is where --json output goes: the command's output before the termsafe
	// wrapper (JSON escapes control characters already, and must stay byte-exact).
	jsonOut io.Writer
}

func NewRoot() *cobra.Command { return newRoot(&app{}) }

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:           "asagao",
		Short:         "Asana をターミナルから操作する TUI / CLI / MCP サーバー（引数なしで TUI を起動）",
		Args:          cobra.NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return a.runTUI(cmd.Context()) },
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Folders of the former name (asana-tui) move to the new ones. What could not
			// be moved is left in place: warn and go on with the new folders.
			if err := config.MigrateLegacyDirs(); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "注意:", termsafe.String(err.Error()))
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			a.cfg = cfg
			// Task names, notes, comments and notifications are printed as they are; strip
			// terminal control characters from everything the commands write. (--json goes
			// to the output as it is: JSON escapes them already.)
			a.jsonOut = cmd.OutOrStdout()
			cmd.SetOut(termsafe.Writer(cmd.OutOrStdout()))
			cmd.SetErr(termsafe.Writer(cmd.ErrOrStderr()))
			return nil
		},
	}
	root.PersistentFlags().BoolVar(&a.json, "json", false, "結果を JSON で出力する")
	root.Flags().BoolVar(&a.offline, "offline", false, "Asana と同期せず、ローカルストアだけで TUI を起動する")
	root.AddCommand(a.authCmd(), a.syncCmd(), a.taskCmd(), a.searchCmd(), a.projectCmd(), a.commentCmd(), a.outboxCmd(), a.mcpCmd(), a.tagCmd(), a.sectionCmd(), a.imageViewCmd(), a.keysCmd(), a.resetCmd(), a.inboxCmd())
	return root
}

func Execute() {
	if err := NewRoot().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", termsafe.String(err.Error()))
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
	c := a.newClient(ctx, src)
	c.AuthHint = src.UnauthorizedHint()
	return c, nil
}

// newClient builds the Asana client. For a login, src must be the *auth.Source itself (not
// its embedded TokenSource): its Token marks a request without a token (*auth.TokenError),
// which is how sync knows it was not sent.
func (a *app) newClient(ctx context.Context, src oauth2.TokenSource) *asana.Client {
	c := asana.NewClient(oauth2.NewClient(ctx, src))
	if a.baseURL != "" {
		c.BaseURL = a.baseURL
	}
	return c
}

// syncer is the Syncer for api and st, with the [sync] settings.
func (a *app) syncer(api *asana.Client, st *store.Store) *sync.Syncer {
	return &sync.Syncer{API: api, Store: st, Cfg: sync.Config(a.cfg.Sync)}
}

func (a *app) storePath() string { return filepath.Join(config.DataDir(), "store.db") }

func (a *app) openStore(ctx context.Context) (*store.Store, error) {
	return store.Open(ctx, a.storePath())
}

// printJSON writes v as indented JSON to the command's output (#25).
func (a *app) printJSON(v any) error {
	w := a.jsonOut
	if w == nil {
		w = os.Stdout
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
