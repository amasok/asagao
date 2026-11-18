package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app"
)

// projectFieldsCmd lists a project's custom fields, and adds a field of the workspace to
// it or removes one (#37). The changes are queued like any other.
func (a *app) projectFieldsCmd() *cobra.Command {
	var add, remove []string
	var all bool
	cmd := &cobra.Command{
		Use:   "fields <プロジェクト> [--all] [--add フィールド]... [--remove フィールド]... [--yes]",
		Short: "カスタムフィールドを表示 / ワークスペースのフィールドを追加 / 外す（オフラインでも積む）",
		Example: `  asagao project fields 新居 --all         # 追加できるワークスペースのフィールドも
  asagao project fields 新居 --add 見積もり
  asagao project fields 新居 --remove 見積もり --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			pf := &usecase.ProjectFields{Store: st, Clock: time.Now}
			if len(remove) > 0 {
				if yes, _ := cmd.Flags().GetBool("yes"); !yes && !a.dryRun {
					return errors.New("フィールドを外すと、このプロジェクトのタスクからそのフィールドと値が外れます。外すには --yes を付けてください（--dry-run で何が変わるか確かめられます）")
				}
			}
			var done []string
			for _, c := range []struct {
				names []string
				add   bool
				verb  string
			}{{add, true, "追加しました"}, {remove, false, "外しました"}} {
				for _, name := range c.names {
					f, err := pf.Set(ctx, args[0], name, c.add)
					if err != nil {
						return err
					}
					done = append(done, fmt.Sprintf("「%s」を%s", f.Name, c.verb))
				}
			}
			if len(done) > 0 {
				msg := "カスタムフィールド" + done[0]
				for _, d := range done[1:] {
					msg += "、" + d
				}
				return a.reportWrite(cmd, st, writeResult{}, msg)
			}
			_, fields, err := pf.List(ctx, args[0], all)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(fields)
			}
			out := cmd.OutOrStdout()
			heading := false
			for _, f := range fields {
				if !f.Attached && !heading {
					fmt.Fprintln(out, "[追加できるフィールド]")
					heading = true
				}
				line := fmt.Sprintf("%s  (%s)  %s", f.Name, f.GID, f.Type)
				if f.Important {
					line += "  [重要]"
				}
				if f.Pending {
					line += "  [未送信]"
				}
				fmt.Fprintln(out, line)
			}
			if len(fields) == 0 {
				fmt.Fprintln(out, "カスタムフィールドはありません（--all でワークスペースのフィールドも表示）")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "このプロジェクトに追加できる、ワークスペースのほかのフィールドも表示する")
	cmd.Flags().StringSliceVar(&add, "add", nil, "追加するフィールド（名前または gid。ワークスペースのフィールドから）")
	cmd.Flags().StringSliceVar(&remove, "remove", nil, "外すフィールド（--yes が必要）")
	cmd.Flags().Bool("yes", false, "確認なしで外す")
	return a.addDryRunFlag(cmd)
}
