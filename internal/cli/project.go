package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app" // the cli package has its own type app
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/sync"
)

func (a *app) projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "プロジェクトの一覧"}
	var all bool
	list := &cobra.Command{
		Use:   "list",
		Short: "プロジェクトとセクションを一覧表示する（デフォルトは参加しているプロジェクト）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			projects, err := query(st).ListProjects(ctx, !all)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(projects)
			}
			out := cmd.OutOrStdout()
			for _, p := range projects {
				fmt.Fprintf(out, "%s  (%s)\n", p.Name, p.GID)
				for _, s := range p.Sections {
					fmt.Fprintf(out, "  - %s  (%s)\n", s.Name, s.GID)
				}
			}
			return nil
		},
	}
	list.Flags().BoolVar(&all, "all", false, "参加していないプロジェクトも表示する")
	cmd.AddCommand(list, a.projectStatusCmd(), a.projectCreateCmd(), a.projectArchiveCmd(), a.projectMembersCmd())
	return cmd
}

// onlineSyncer returns a syncer with an API client (for online-only project operations).
func (a *app) onlineSyncer(ctx context.Context, st Store) (*sync.Syncer, error) {
	if a.dryRun {
		return nil, errDryRunSend
	}
	api, err := a.apiClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("Asana に接続できません: %w", err)
	}
	return a.syncer(api, st), nil
}

func (a *app) projectStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <プロジェクト>",
		Short: "ステータス更新を表示する（新しい順）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			gid, err := query(st).ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			if s, err := a.onlineSyncer(ctx, st); err == nil {
				_ = s.FetchStatusUpdates(ctx, gid)
			}
			updates, err := query(st).StatusUpdates(ctx, gid)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(updates)
			}
			if len(updates) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "ステータス更新はまだありません")
			}
			for _, u := range updates {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s  %s %s\n%s\n\n", readmodel.StatusLabel(u.StatusType), u.Title, u.Author, u.CreatedAt, indent(u.Text))
			}
			return nil
		},
	}
	var status, title, text string
	post := &cobra.Command{
		Use:   "post <プロジェクト> --status <on_track|at_risk|off_track|on_hold|complete> --text <本文>",
		Short: "ステータス更新を投稿する（本文はマークダウン）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			statuses := usecase.Status{Store: st, Text: richtext.Converter{}, Clock: time.Now}
			if _, err := statuses.Create(ctx, usecase.StatusInput{Project: args[0], Status: status, Title: title, Text: text}); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "ステータスを投稿しました")
		},
	}
	post.Flags().StringVar(&status, "status", "on_track", "on_track / at_risk / off_track / on_hold / complete")
	post.Flags().StringVar(&title, "title", "", "タイトル")
	post.Flags().StringVar(&text, "text", "", "本文（マークダウン）")
	cmd.AddCommand(a.addDryRunFlag(post))
	return cmd
}

func (a *app) projectCreateCmd() *cobra.Command {
	return a.addDryRunFlag(&cobra.Command{
		Use:   "create <名前>",
		Short: "プロジェクトを作る（オンラインのみ）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			name := strings.Join(args, " ")
			if a.dryRun {
				return a.dryRunPlan(cmd, plannedChange{Kind: "create_project", Description: "プロジェクト「" + name + "」の作成"})
			}
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			p, err := s.CreateProject(ctx, name)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "プロジェクトを作りました: %s (%s)\n", p.Name, p.GID)
			return nil
		},
	})
}

func (a *app) projectArchiveCmd() *cobra.Command {
	var undo bool
	cmd := &cobra.Command{
		Use:   "archive <プロジェクト>",
		Short: "プロジェクトをアーカイブする（--undo で戻す。オンラインのみ）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			gid, err := query(st).ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			if a.dryRun {
				kind, what := "archive_project", "アーカイブ"
				if undo {
					kind, what = "unarchive_project", "アーカイブの解除"
				}
				return a.dryRunPlan(cmd, plannedChange{Kind: kind, TaskGID: gid, Description: fmt.Sprintf("プロジェクト「%s」の%s", projectName(ctx, st, gid), what)})
			}
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			if err := s.SetProjectArchived(ctx, gid, !undo); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), map[bool]string{true: "アーカイブを解除しました", false: "アーカイブしました"}[undo])
			return nil
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "アーカイブを解除する")
	return a.addDryRunFlag(cmd)
}

func (a *app) projectMembersCmd() *cobra.Command {
	var add, remove []string
	cmd := &cobra.Command{
		Use:   "members <プロジェクト> [--add ユーザー]... [--remove ユーザー]...",
		Short: "メンバーを表示 / 追加 / 外す（変更はオンラインのみ）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			gid, err := query(st).ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			if len(add)+len(remove) > 0 {
				var addG, remG []string
				for _, u := range add {
					g, err := query(st).ResolveUser(ctx, u)
					if err != nil {
						return err
					}
					addG = append(addG, g)
				}
				for _, u := range remove {
					g, err := query(st).ResolveUser(ctx, u)
					if err != nil {
						return err
					}
					remG = append(remG, g)
				}
				if a.dryRun {
					pname := projectName(ctx, st, gid)
					var planned []plannedChange
					for i, g := range addG {
						planned = append(planned, plannedChange{Kind: "add_member", TaskGID: gid, Description: fmt.Sprintf("プロジェクト「%s」にメンバー「%s」を追加", pname, add[i]), Payload: map[string]any{"user": g}})
					}
					for i, g := range remG {
						planned = append(planned, plannedChange{Kind: "remove_member", TaskGID: gid, Description: fmt.Sprintf("プロジェクト「%s」からメンバー「%s」を外す", pname, remove[i]), Payload: map[string]any{"user": g}})
					}
					return a.dryRunPlan(cmd, planned...)
				}
				s, err := a.onlineSyncer(ctx, st)
				if err != nil {
					return err
				}
				if err := s.SetProjectMembers(ctx, gid, addG, remG); err != nil {
					return err
				}
			}
			members, err := query(st).ProjectMembers(ctx, gid)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(members)
			}
			for _, u := range members {
				fmt.Fprintln(cmd.OutOrStdout(), u.Name)
			}
			return nil
		},
	}
	cmd.Flags().StringSliceVar(&add, "add", nil, "追加するユーザー（me・名前・gid）")
	cmd.Flags().StringSliceVar(&remove, "remove", nil, "外すユーザー")
	return a.addDryRunFlag(cmd)
}
