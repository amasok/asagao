package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
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
			projects, err := st.ListProjects(ctx, !all)
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
func (a *app) onlineSyncer(ctx context.Context, st *store.Store) (*sync.Syncer, error) {
	api, err := a.apiClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("Asana に接続できません: %w", err)
	}
	return &sync.Syncer{API: api, Store: st, Cfg: a.cfg.Sync}, nil
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
			gid, err := st.ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			if s, err := a.onlineSyncer(ctx, st); err == nil {
				_ = s.FetchStatusUpdates(ctx, gid)
			}
			updates, err := st.StatusUpdates(ctx, gid)
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
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s  %s %s\n%s\n\n", store.StatusLabel(u.StatusType), u.Title, u.Author, u.CreatedAt, indent(u.Text))
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
			gid, err := st.ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			me, err := st.Me(ctx)
			if err != nil {
				return err
			}
			if _, err := st.CreateStatusLocal(ctx, gid, status, title, richtext.StatusUpdate.PlainFromMarkdown(text), richtext.StatusUpdate.FromMarkdown(text), me, time.Now()); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "ステータスを投稿しました")
		},
	}
	post.Flags().StringVar(&status, "status", "on_track", "on_track / at_risk / off_track / on_hold / complete")
	post.Flags().StringVar(&title, "title", "", "タイトル")
	post.Flags().StringVar(&text, "text", "", "本文（マークダウン）")
	cmd.AddCommand(post)
	return cmd
}

func (a *app) projectCreateCmd() *cobra.Command {
	return &cobra.Command{
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
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			p, err := s.CreateProject(ctx, strings.Join(args, " "))
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "プロジェクトを作りました: %s (%s)\n", p.Name, p.GID)
			return nil
		},
	}
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
			gid, err := st.ResolveProject(ctx, args[0])
			if err != nil {
				return err
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
	return cmd
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
			gid, err := st.ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			if len(add)+len(remove) > 0 {
				s, err := a.onlineSyncer(ctx, st)
				if err != nil {
					return err
				}
				var addG, remG []string
				for _, u := range add {
					g, err := st.ResolveUser(ctx, u)
					if err != nil {
						return err
					}
					addG = append(addG, g)
				}
				for _, u := range remove {
					g, err := st.ResolveUser(ctx, u)
					if err != nil {
						return err
					}
					remG = append(remG, g)
				}
				if err := s.SetProjectMembers(ctx, gid, addG, remG); err != nil {
					return err
				}
			}
			members, err := st.ProjectMembers(ctx, gid)
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
	return cmd
}
