package cli

import (
	"context"
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
  claude mcp add asana -- asana-tui mcp --read-only   # 書き込みツールを無効化

読み取りはローカルストアから行うので、高速でオフラインでも動きます。
書き込みは TUI と同じ「未送信の変更」を通り、オンラインならすぐに Asana へ送ります。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			s := &mcpserver.Server{
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
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "書き込みツール（create_task / update_task / add_comment）を無効にする")
	return cmd
}
