package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/sync"
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
			if errors.Is(err, sync.ErrAccountChanged) {
				return accountChanged(ctx, st)
			}
			if errors.Is(err, sync.ErrPushBusy) { // not offline: someone else is syncing
				if a.json {
					return a.printJSON(res)
				}
				fmt.Fprintln(cmd.OutOrStdout(), "別の asagao（TUI・MCP サーバーなど）が同期中のため、今回は何もしませんでした。未送信の変更はそちらで送られます")
				return nil
			}
			if err != nil {
				return err
			}
			if a.json {
				// res.Push.Discarded carries this push's dropped changes; mark those as shown
				ids := make([]int64, len(res.Push.Discarded))
				for i, d := range res.Push.Discarded {
					ids[i] = d.ID
				}
				_ = st.MarkDiscardsNotified(ctx, ids)
				return a.printJSON(res)
			}
			takeDiscards(ctx, st, cmd.OutOrStdout(), true)
			kind := "変更分"
			if res.Full {
				kind = "全件"
			}
			if p := res.Push; p.Sent+p.Conflicts+p.Failed > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "未送信の変更: 送信 %d / 競合 %d / 失敗 %d\n", p.Sent, p.Conflicts, p.Failed)
				if p.Conflicts+p.Failed > 0 {
					fmt.Fprintln(cmd.OutOrStdout(), "  → `asagao outbox list` で確認してください")
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "同期しました（%s）: ワークスペース %d / プロジェクト %d / 取得したタスク %d / 削除 %d（%s）\n",
				kind, res.Workspaces, res.Projects, res.Tasks, res.Pruned, res.Duration.Round(100*time.Millisecond))
			if inbox, err := query(st).InboxState(ctx); err == nil && !inbox.Available() {
				fmt.Fprintln(cmd.ErrOrStderr(), sync.InboxUnavailableMessage(inbox, a.cfg.Auth.Scopes))
			}
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
			stats, err := query(st).Stats(cmd.Context())
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
			if p, c, f, err := query(st).OutboxCounts(cmd.Context()); err == nil && p+c+f > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "未送信 %d / 競合 %d / 失敗 %d（`asagao outbox list`）\n", p, c, f)
			}
			return nil
		},
	})
	return cmd
}
