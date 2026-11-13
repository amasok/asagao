package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/sync"
)

func (a *app) syncCmd() *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Asana のデータをローカルストアに同期する（前回以降の変更分。24 時間ごとに全件）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			api, err := a.apiClient(ctx)
			if err != nil {
				return err
			}
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			s := &sync.Syncer{API: api, Store: st, Cfg: a.cfg.Sync}
			if !a.json {
				s.Progress = func(f string, args ...any) { fmt.Fprintf(cmd.ErrOrStderr(), "  "+f+"\n", args...) }
			}
			res, err := s.Run(ctx, full)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(res)
			}
			kind := "変更分"
			if res.Full {
				kind = "全件"
			}
			if p := res.Push; p.Sent+p.Conflicts+p.Failed > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "未送信の変更: 送信 %d / 競合 %d / 失敗 %d\n", p.Sent, p.Conflicts, p.Failed)
				if p.Conflicts+p.Failed > 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "  → `asana-tui outbox list` で確認してください")
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "同期しました（%s）: ワークスペース %d / プロジェクト %d / 取得したタスク %d / 削除 %d（%s）\n",
				kind, res.Workspaces, res.Projects, res.Tasks, res.Pruned, res.Duration.Round(100*time.Millisecond))
			return nil
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "全件同期する（Asana で削除されたタスクもローカルから消す）")
	cmd.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "ローカルストアの状態を表示する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			stats, err := st.Stats(cmd.Context())
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(stats)
			}
			last := "未同期"
			if stats.LastSync != nil {
				last = stats.LastSync.Local().Format("2006-01-02 15:04:05")
			}
			fmt.Fprintf(cmd.OutOrStdout(), "最終同期: %s\nワークスペース %d / プロジェクト %d / セクション %d / タスク %d（未完了 %d）\n",
				last, stats.Workspaces, stats.Projects, stats.Sections, stats.Tasks, stats.OpenTasks)
			if p, c, f, err := st.OutboxCounts(cmd.Context()); err == nil && p+c+f > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "未送信 %d / 競合 %d / 失敗 %d（`asana-tui outbox list`）\n", p, c, f)
			}
			return nil
		},
	})
	return cmd
}
