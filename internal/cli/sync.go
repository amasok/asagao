package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/sync"
)

func (a *app) syncCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Asana のデータをローカルストアに同期する",
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
			res, err := s.Full(ctx)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "同期しました: ワークスペース %d / プロジェクト %d / タスク %d（%s）\n",
				res.Workspaces, res.Projects, res.Tasks, res.Duration.Round(100*time.Millisecond))
			return nil
		},
	}
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
			return nil
		},
	})
	return cmd
}
