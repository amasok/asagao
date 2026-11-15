package cli

import (
	"context"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/mcpserver"
	"github.com/amasok/asana-tui/internal/sync"
)

func (a *app) mcpCmd() *cobra.Command {
	var readOnly bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "MCP サーバーとして起動する（stdio）",
		Long: `Claude Code などの MCP クライアントから asana-tui のデータを使うための MCP サーバーです。

登録例（Claude Code）:
  claude mcp add asana -- asana-tui mcp
  claude mcp add asana -- asana-tui mcp --read-only   # 書き込みツールを無効化（sync は取得のみ）

読み取りはローカルストアから行うので、高速でオフラインでも動きます。
書き込みは TUI と同じ「未送信の変更」を通り、オンラインならすぐに Asana へ送ります。

upload_attachment で送れるのは、起動したフォルダ（/・ホームフォルダ・その隠しフォルダを除く）、
~/Downloads、~/Desktop と、config.toml の [mcp] upload_dirs に書いたフォルダの中のファイルだけです。
隠しファイル・隠しフォルダと、asana-tui 自身の設定・データ・キャッシュは送れません。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			cwd, _ := os.Getwd()
			home, _ := os.UserHomeDir()
			upload := mcpserver.NewUploadPolicy(cwd, home, a.cfg.MCP.ExpandedUploadDirs(),
				[]string{config.ConfigDir(), config.DataDir(), config.CacheDir()})
			s := &mcpserver.Server{
				Upload:     upload,
				Store:      st,
				ReadOnly:   readOnly,
				CacheDir:   filepath.Join(config.CacheDir(), "attachments"),
				Version:    version,
				AuthScopes: a.cfg.Auth.Scopes,
				// The syncer outlives the tool call that creates it, so build it with the
				// server's context, not the request's (token refresh may write to 1Password).
				NewSyncer: func(context.Context) (*sync.Syncer, error) {
					api, err := a.apiClient(ctx)
					if err != nil {
						return nil, err
					}
					return &sync.Syncer{API: api, Store: st, Cfg: a.cfg.Sync}, nil
				},
			}
			return s.New().Run(ctx, &mcp.StdioTransport{})
		},
	}
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "書き込みツール（create_task / update_task / add_comment など）を無効にし、sync は取得だけにする（未送信の変更は送らない）")
	return cmd
}
