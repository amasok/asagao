package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

// backupDir is where unsent changes are written before a reset.
func backupDir() string { return filepath.Join(config.DataDir(), "backups") }

func (a *app) resetCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "reset",
		Short: "ローカルのデータを初期化する（別の Asana アカウントで使うとき）",
		Long: "タスク・プロジェクト・未送信の変更など、ローカルのデータをすべて消します。ログイン情報は消しません。\n" +
			"次の sync で、ログイン中のアカウントのデータを取り込み直します。\n" +
			"未送信の変更があるときは --force が必要です。消す前に、未送信の変更と破棄した変更の記録を\n" +
			"JSON ファイルに自動で書き出します（書き出せなければ何も消しません）。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			res, err := st.ResetWithBackup(ctx, backupDir(), force, time.Now())
			switch {
			case errors.Is(err, store.ErrUnsentChanges):
				return fmt.Errorf("Asana に送っていない変更が %d 件あり、初期化すると消えます（消す前に自動でバックアップします）。"+
					"内容は `asana-tui outbox list` で確認できます。よろしければ --force を付けて実行してください", res.Unsent)
			case errors.Is(err, store.ErrPushBusy):
				return errors.New("別の asana-tui が同期・送信中のため初期化できません。終わってからやり直してください")
			case err != nil:
				return err
			}
			if a.json {
				return a.printJSON(res)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "ローカルのデータを初期化しました。次の `asana-tui sync` で、ログイン中のアカウントのデータを取り込みます")
			if res.BackupPath != "" {
				fmt.Fprintf(out, "消す前に、未送信の変更 %d 件と破棄した変更の記録を書き出しました: %s\n", res.Unsent, res.BackupPath)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "未送信の変更があっても初期化する（自動でバックアップしてから消す）")
	return cmd
}

// accountChanged explains sync.ErrAccountChanged with the number of unsent changes.
func accountChanged(ctx context.Context, st *store.Store) error {
	n, _ := st.UnsentCount(ctx)
	return fmt.Errorf("%s: %w", store.AccountChangedHelp(n, "asana-tui reset"), sync.ErrAccountChanged)
}
