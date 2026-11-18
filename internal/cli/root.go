// Package cli wires the cobra commands.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app" // the cli package has its own type app
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/settings"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/termsafe"
	"github.com/amasok/asagao/internal/xdg"
)

type app struct {
	cfg     settings.Config
	json    bool
	offline bool
	// dryRun (--dry-run of a write command) makes openStore return a throwaway copy of the
	// store and reportWrite show what would be queued instead of sending it (dryrun.go).
	dryRun bool
	// noContext (--no-context) ignores .asana.json (context.go).
	noContext bool
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
		Version:       currentBuild().String(),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			// Folders of the former name (asana-tui) move to the new ones. What could not
			// be moved is left in place: warn and go on with the new folders.
			if err := wiring.MigrateLegacyDirs(); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "注意:", termsafe.String(err.Error()))
			}
			cfg, err := wiring.LoadSettings()
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
	root.PersistentFlags().BoolVar(&a.noContext, "no-context", false, ".asana.json（作業中のプロジェクト・タスク）を使わない")
	root.Flags().BoolVar(&a.offline, "offline", false, "Asana と同期せず、ローカルストアだけで TUI を起動する")
	root.AddCommand(a.contextCmd(), a.agentGuideCmd())
	root.AddCommand(a.authCmd(), a.syncCmd(), a.taskCmd(), a.searchCmd(), a.projectCmd(), a.commentCmd(), a.outboxCmd(), a.mcpCmd(), a.tagCmd(), a.sectionCmd(), a.imageViewCmd(), a.keysCmd(), a.resetCmd(), a.inboxCmd(), a.searchesCmd(), a.apiCmd(), a.templateCmd())
	root.AddCommand(a.gitCmd())
	return root
}

// Wiring is what the commands get from internal/wire: the entry point does not build the
// adapters (the store, the Asana client) itself (architecture.md, 依存の決まり 4 and 6).
// cmd/asagao fills it with wire's functions; the package's tests do the same.
type Wiring struct {
	// MigrateLegacyDirs moves the folders of the former name (config.MigrateLegacyDirs).
	MigrateLegacyDirs func() error
	// LoadSettings reads config.toml (config.Load).
	LoadSettings func() (settings.Config, error)
	// APIClient is the Asana client for the login of cfg; baseURL overrides Asana's URL.
	APIClient func(ctx context.Context, cfg settings.Config, baseURL string) (sync.AsanaAPI, error)
	// Syncer is the Syncer for api (nil: offline) and st, with the settings of cfg.
	Syncer func(api sync.AsanaAPI, st sync.LocalStore, cfg settings.Config) *sync.Syncer
	// OpenStore opens the local store at path (a nil Store with the error).
	OpenStore func(ctx context.Context, path string) (Store, error)
	// CheckPAT asks Asana whose a Personal Access Token is, retrying once after retryDelay.
	CheckPAT func(ctx context.Context, token, baseURL string, retryDelay time.Duration, onRetry func(status int, wait time.Duration)) (*model.User, error)
	// Searches is searches.toml, the saved searches (config.SearchFile).
	Searches usecase.SearchFile
}

// wiring is set by Execute (and by the tests' init).
var wiring Wiring

func Execute(w Wiring) {
	wiring = w
	if err := NewRoot().ExecuteContext(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", termsafe.String(err.Error()))
		os.Exit(1)
	}
}

func (a *app) tokenStore() (auth.TokenStore, error) { return auth.OpenTokenStore(a.cfg.Auth) }

// apiClient is the Asana client for the login (wire.APIClient).
func (a *app) apiClient(ctx context.Context) (sync.AsanaAPI, error) {
	return wiring.APIClient(ctx, a.cfg, a.baseURL)
}

// syncer is the Syncer for api and st (wire.Syncer).
func (a *app) syncer(api sync.AsanaAPI, st Store) *sync.Syncer {
	return wiring.Syncer(api, st, a.cfg)
}

func (a *app) storePath() string { return filepath.Join(xdg.DataDir(), "store.db") }

func (a *app) openStore(ctx context.Context) (Store, error) {
	st, err := wiring.OpenStore(ctx, a.storePath())
	if err != nil || !a.dryRun {
		return st, err
	}
	return openDryRun(ctx, st)
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
