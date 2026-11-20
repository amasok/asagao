package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/domain/tag"
)

// Commands that change sections and tags themselves (#33). Deleting cannot be undone in
// Asana, so `rm` asks for --yes (--dry-run shows what it would do without it).

func (a *app) sectionCreateCmd() *cobra.Command {
	var project, before, after string
	cmd := &cobra.Command{
		Use:   "create <名前> --project <プロジェクト> [--before <セクション> | --after <セクション>]",
		Short: "セクションを作る（オフラインでも作れ、すぐにタスクを移せる）",
		Example: `  asagao section create レビュー待ち --project 新居
  asagao section create 保留 --project 新居 --after 完了`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			name := strings.Join(args, " ")
			sections := st.App.Sections(time.Now)
			gid, err := sections.Create(ctx, project, name, usecase.SectionPlace{Before: before, After: after})
			if err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{SectionGID: gid}, "セクションを作りました: "+name)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "プロジェクト（gid または名前）")
	cmd.Flags().StringVar(&before, "before", "", "このセクションの前に置く（gid または名前）")
	cmd.Flags().StringVar(&after, "after", "", "このセクションの後に置く（gid または名前）")
	_ = cmd.MarkFlagRequired("project")
	cmd.MarkFlagsMutuallyExclusive("before", "after")
	return a.addDryRunFlag(cmd)
}

func (a *app) sectionRmCmd() *cobra.Command {
	var project string
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <セクション> --project <プロジェクト> --yes",
		Aliases: []string{"delete"},
		Short:   "空のセクションを削除する（取り消せないので --yes が要る）",
		Example: `  asagao section rm 保留 --project 新居 --dry-run
  asagao section rm 保留 --project 新居 --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			sections := st.App.Sections(time.Now)
			_, gid, err := sections.ResolveIn(ctx, project, args[0])
			if err != nil {
				return err
			}
			name, err := sections.Store.SectionName(ctx, gid)
			if err != nil {
				return err
			}
			if !yes && !a.dryRun {
				return fmt.Errorf("セクション「%s」を削除します。Asana でも取り消せません。よろしければ --yes を付けて実行してください（--dry-run で確かめられます）", name)
			}
			if err := sections.DeleteHere(ctx, gid); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "セクションを削除しました: "+name)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "プロジェクト（gid または名前）")
	cmd.Flags().BoolVar(&yes, "yes", false, "確認なしで削除する")
	_ = cmd.MarkFlagRequired("project")
	return a.addDryRunFlag(cmd)
}

func (a *app) sectionMoveCmd() *cobra.Command {
	var project, before, after string
	cmd := &cobra.Command{
		Use:     "move <セクション> --project <プロジェクト> (--before <セクション> | --after <セクション>)",
		Short:   "セクションを並べ替える",
		Example: `  asagao section move 保留 --project 新居 --before 完了`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			sections := st.App.Sections(time.Now)
			if err := sections.Reorder(ctx, project, args[0], usecase.SectionPlace{Before: before, After: after}); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "セクションを並べ替えました: "+args[0])
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "プロジェクト（gid または名前）")
	cmd.Flags().StringVar(&before, "before", "", "このセクションの前に移す（gid または名前）")
	cmd.Flags().StringVar(&after, "after", "", "このセクションの後に移す（gid または名前）")
	_ = cmd.MarkFlagRequired("project")
	cmd.MarkFlagsMutuallyExclusive("before", "after")
	cmd.MarkFlagsOneRequired("before", "after")
	return a.addDryRunFlag(cmd)
}

func (a *app) tagRenameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rename <タグ> <新しい名前>",
		Short:   "タグの名前を変える",
		Example: `  asagao tag rename 急ぎ 至急`,
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			name := strings.Join(args[1:], " ")
			if err := st.App.Tags(time.Now).Rename(ctx, args[0], name); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "タグの名前を変えました: "+name)
		},
	}
	return a.addDryRunFlag(cmd)
}

func (a *app) tagColorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "color <タグ> <色>",
		Short: "タグの色を変える（none で色なし）",
		Long: "タグの色を変えます。使える色: none, " + strings.Join(tag.Colors, ", ") +
			"\nAsana が表示する新しい名前 (" + strings.Join(tag.NewColors(), ", ") + ") も使えます。" +
			"同じ色の古い名前にして送ります（blue・cool-gray・yellow のように 2 色が同じ名前のものは、上の一覧で先の方）。",
		Example: `  asagao tag color 急ぎ dark-red
  asagao tag color 急ぎ red
  asagao tag color 急ぎ none`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			if err := st.App.Tags(time.Now).SetColor(ctx, args[0], args[1]); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "タグの色を変えました: "+args[1])
		},
	}
	return a.addDryRunFlag(cmd)
}

func (a *app) tagRmCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:     "rm <タグ> --yes",
		Aliases: []string{"delete"},
		Short:   "タグを削除する（すべてのタスクから外れる。取り消せないので --yes が要る）",
		Example: `  asagao tag rm 古いタグ --dry-run
  asagao tag rm 古いタグ --yes`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			tags := st.App.Tags(time.Now)
			t, err := tags.Resolve(ctx, args[0])
			if err != nil {
				return err
			}
			if !yes && !a.dryRun {
				return fmt.Errorf("タグ「%s」を削除します（このキャッシュにあるタスクでは %d 件から外れます）。Asana でも取り消せません。よろしければ --yes を付けて実行してください（--dry-run で確かめられます）", t.Name, t.Total)
			}
			if err := tags.DeleteHere(ctx, t.GID); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, fmt.Sprintf("タグを削除しました: %s（キャッシュにあるタスク %d 件から外しました）", t.Name, t.Total))
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "確認なしで削除する")
	return a.addDryRunFlag(cmd)
}

func (a *app) sectionMineCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mine",
		Short: "マイタスクのセクション（今日やる・あとで など）を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			secs, err := st.App.Sections(time.Now).MySections(ctx, "")
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(secs)
			}
			if len(secs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "マイタスクのセクションはまだ分かりません（同期のあとに表示します）")
				return nil
			}
			rows := [][]string{}
			for _, s := range secs {
				rows = append(rows, []string{s.Name, s.GID})
			}
			printTable(cmd.OutOrStdout(), rows)
			return nil
		},
	}
}
