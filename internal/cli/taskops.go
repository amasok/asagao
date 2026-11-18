package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

func taskOpsUseCases(st Store) *usecase.TaskOps {
	return &usecase.TaskOps{Store: st, Clock: time.Now}
}

// deleteSummary says what deleting a task takes with it, for the confirmation.
func deleteSummary(p readmodel.DeletePlan) string {
	s := fmt.Sprintf("「%s」", p.Name)
	if p.Subtasks > 0 {
		s += fmt.Sprintf("とサブタスク %d 件", p.Subtasks)
	}
	if p.Queued > 0 {
		s += fmt.Sprintf("（未送信の変更 %d 件も消えます）", p.Queued)
	}
	return s
}

// taskRemoveCmd deletes a task (#32). Deleting is confirmed with --yes; --dry-run shows
// the change without it.
func (a *app) taskRemoveCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm <gid>",
		Short: "タスクを削除する（サブタスクも。--yes で確認）",
		Long: "タスクをサブタスクごと Asana から削除します。Asana のごみ箱にしばらく残ります。\n" +
			"送るまではタスクを一覧から隠すだけで、`asagao outbox discard` で取り消せます。\n" +
			"まだ Asana に送っていないタスクは、その場で消えます。",
		Example: `  asagao task rm 1203455468597469 --dry-run
  asagao task rm 1203455468597469 --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			ops := taskOpsUseCases(st)
			plan, err := ops.PlanDelete(ctx, args[0])
			if err != nil {
				return err
			}
			if !yes && !a.dryRun {
				return fmt.Errorf("%sを削除します。よろしければ --yes を付けて実行してください（--dry-run で、積まれる変更を確かめられます）", deleteSummary(plan))
			}
			if _, err := ops.Delete(ctx, args[0]); err != nil {
				return err
			}
			gid := args[0]
			if plan.Local {
				gid = "" // gone: nothing to send for it
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: gid}, "削除しました: "+deleteSummary(plan))
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "確認なしで削除する")
	return a.addDryRunFlag(cmd)
}

// taskParentCmd makes a task a subtask of another, or a top-level task (#32).
func (a *app) taskParentCmd() *cobra.Command {
	var none bool
	var before, after string
	cmd := &cobra.Command{
		Use:   "parent <gid> (<親タスクの gid> | --none) [--before <gid> | --after <gid>]",
		Short: "親タスクを付け替える（--none で普通のタスクに戻す）",
		Example: `  asagao task parent 1203455468597469 1203455468590000
  asagao task parent 1203455468597469 1203455468590000 --after 1203455468591111
  asagao task parent 1203455468597469 --none`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			parent := ""
			switch {
			case len(args) == 2 && none:
				return errors.New("親タスクと --none は、どちらか一方だけ指定してください")
			case len(args) == 2:
				parent = args[1]
			case !none:
				return errors.New("親タスクの gid か --none を指定してください")
			}
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := taskOpsUseCases(st).SetParent(ctx, args[0], parent, model.Position{Before: before, After: after}); err != nil {
				return err
			}
			msg := "親タスクを付け替えました"
			if parent == "" {
				msg = "親タスクから外しました"
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, msg)
		},
	}
	cmd.Flags().BoolVar(&none, "none", false, "親タスクから外す")
	cmd.Flags().StringVar(&before, "before", "", "このサブタスクの前に入れる（gid）")
	cmd.Flags().StringVar(&after, "after", "", "このサブタスクの後に入れる（gid）")
	cmd.MarkFlagsMutuallyExclusive("before", "after")
	return a.addDryRunFlag(cmd)
}
