package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// timeCmd is `asagao time`: the time tracking entries of tasks (#6).
func (a *app) timeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "time", Short: "タスクの作業時間（タイムトラッキング。Asana の有料プラン）"}
	tracking := func(st Store) *usecase.TimeTracking { return &usecase.TimeTracking{Store: st, Clock: time.Now} }

	var offline bool
	list := &cobra.Command{
		Use:   "list <タスクの gid>",
		Short: "タスクに記録した作業時間と合計を表示する",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			tt := tracking(st)
			if !tt.Available(ctx) {
				return &usecase.ErrPlanUnavailable{Feature: readmodel.PlanTimeTracking}
			}
			var stale error
			if !offline {
				if s, err := a.onlineSyncer(ctx, st); err == nil {
					stale = s.FetchTimeEntries(ctx, args[0])
				} else {
					stale = err
				}
				if usecase.IsPlanUnavailable(stale) {
					return stale
				}
			}
			es, err := tt.List(ctx, args[0])
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(es)
			}
			printTimeEntries(cmd.OutOrStdout(), es)
			if stale != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "（Asana から取れなかったため、キャッシュの内容です: %v）\n", stale)
			}
			return nil
		},
	}
	list.Flags().BoolVar(&offline, "offline", false, "Asana に問い合わせず、ローカルストアだけで表示する")

	var day string
	add := a.addDryRunFlag(&cobra.Command{
		Use:   "add <タスクの gid> <時間>",
		Short: "作業時間を記録する（時間は 90 / 90m / 1h30m / 1.5h / 1:30）",
		Example: "  asagao time add 1209876543210 1h30m\n" +
			"  asagao time add 1209876543210 45m --date yesterday",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			gid, err := tracking(st).Add(ctx, args[0], args[1], day)
			if err != nil {
				return flagError(err)
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0], TimeEntryID: gid}, "作業時間を記録しました")
		},
	})
	add.Flags().StringVar(&day, "date", "", "記録する日（YYYY-MM-DD / today / yesterday / -Nd。省略すると今日）")

	edit := a.writeCommand("edit <記録の gid> [--duration 時間] [--date 日]", "記録した作業時間・日を直す", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			duration, _ := cmd.Flags().GetString("duration")
			on, _ := cmd.Flags().GetString("date")
			if duration == "" && on == "" {
				return "", "", fmt.Errorf("--duration か --date を指定してください")
			}
			return "", "作業時間を直しました", flagError(tracking(st).Edit(cmd.Context(), args[0], duration, on))
		})
	edit.Flags().String("duration", "", "新しい時間（90 / 1h30m など）")
	edit.Flags().String("date", "", "新しい日（YYYY-MM-DD / today / yesterday / -Nd）")
	edit.Example = "  asagao time edit 1209876543210 --duration 2h   # gid は time list で表示されます"

	del := a.writeCommand("delete <記録の gid> --yes", "記録した作業時間を削除する（取り消せません）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			if err := a.confirmDelete(cmd, "作業時間の記録 "+args[0]); err != nil {
				return "", "", err
			}
			return "", "作業時間の記録を削除しました", tracking(st).Delete(cmd.Context(), args[0])
		})
	del.Flags().Bool("yes", false, "確認なしで削除する")

	cmd.AddCommand(list, add, edit, del)
	return cmd
}

// flagError names the flag or argument of an invalid input (usecase.FieldError).
func flagError(err error) error {
	fe, ok := err.(*usecase.FieldError)
	if !ok {
		return err
	}
	switch fe.Field {
	case "duration":
		return fmt.Errorf("時間: %w", fe.Err)
	case "date":
		return fmt.Errorf("--date: %w", fe.Err)
	}
	return err
}

// printTimeEntries prints a task's entries, newest day first, and their total.
func printTimeEntries(out io.Writer, es readmodel.TimeEntries) {
	if len(es.Entries) == 0 {
		fmt.Fprintln(out, "作業時間の記録はありません")
		return
	}
	rows := [][]string{{"GID", "日付", "時間", "記録した人", "プロジェクト", ""}}
	for _, e := range es.Entries {
		who := ""
		if e.Assignee != nil {
			who = e.Assignee.Name
		} else if e.CreatedBy != nil {
			who = e.CreatedBy.Name
		}
		mark := ""
		if e.Pending {
			mark = "未送信"
		}
		rows = append(rows, []string{e.GID, e.EnteredOn, e.Duration, who, e.Project, mark})
	}
	printTable(out, rows)
	fmt.Fprintf(out, "合計 %s（%d 件）\n", es.Total, len(es.Entries))
}
