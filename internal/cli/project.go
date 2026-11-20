package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app" // the cli package has its own type app
	"github.com/amasok/asagao/internal/domain/project"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/ui/format"
)

func (a *app) projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "プロジェクトの一覧・概要・編集"}
	var all bool
	var team string
	list := &cobra.Command{
		Use:   "list",
		Short: "プロジェクトとセクションを一覧表示する（お気に入り→チームの順。デフォルトは参加しているプロジェクト）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			projects, err := query(st).ListProjectsInTeam(ctx, !all, team)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(projects)
			}
			out := cmd.OutOrStdout()
			// Under headings (favorites, then each team) when there is more than one.
			groups := map[string]bool{}
			for _, p := range projects {
				groups[format.ProjectGroup(p)] = true
			}
			group := ""
			for _, p := range projects {
				if g := format.ProjectGroup(p); len(groups) > 1 && g != group {
					group = g
					fmt.Fprintf(out, "[%s]\n", g)
				}
				mark := ""
				if p.Unfetched {
					mark = "  [タスク未取得]"
				}
				fmt.Fprintf(out, "%s  (%s)%s\n", p.Name, p.GID, mark)
				for _, s := range p.Sections {
					fmt.Fprintf(out, "  - %s  (%s)\n", s.Name, s.GID)
				}
			}
			return nil
		},
	}
	list.Flags().BoolVar(&all, "all", false, "参加していないプロジェクトも表示する")
	list.Flags().StringVar(&team, "team", "", "このチームのプロジェクトだけ（gid または名前。部分一致可）")
	cmd.AddCommand(list, a.projectTeamsCmd(), a.projectShowCmd(), a.projectEditCmd(), a.projectBriefCmd(), a.projectStatusCmd(),
		a.projectCreateCmd(), a.projectDuplicateCmd(), a.projectArchiveCmd(), a.projectMembersCmd(), a.projectFieldsCmd())
	return cmd
}

func (a *app) projectTeamsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "teams",
		Short: "プロジェクトを作れるチーム（参加しているチーム）を表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			teams, err := query(st).ListTeams(ctx)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(teams)
			}
			if len(teams) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "チームはありません（チームのないワークスペースか、まだ同期していません）")
			}
			for _, t := range teams {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  (%s)\n", t.Name, t.GID)
			}
			return nil
		},
	}
}

func (a *app) projectShowCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "show <プロジェクト>",
		Short: "プロジェクトの概要（説明・オーナー・期日・進み具合・最新のステータス・ブリーフ）を表示する",
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
			var stale error
			if !offline {
				if s, err := a.onlineSyncer(ctx, st); err == nil {
					_ = s.FetchStatusUpdates(ctx, gid)
					stale = s.FetchProjectOverview(ctx, gid)
				}
			}
			o, err := query(st).ProjectOverview(ctx, gid)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(o)
			}
			printProjectOverview(cmd.OutOrStdout(), o)
			if stale != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "（Asana から取れなかったため、一部はキャッシュの内容です: %v）\n", stale)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "Asana に問い合わせず、ローカルストアだけで表示する")
	return cmd
}

// printProjectOverview prints a project's overview as text.
func printProjectOverview(out io.Writer, o *readmodel.ProjectOverview) {
	title := o.Name
	if o.Favorite {
		title = "★ " + title
	}
	fmt.Fprintf(out, "%s  (%s)\n", title, o.GID)
	line := func(label, v string) {
		if v != "" {
			fmt.Fprintf(out, "  %s: %s\n", label, v)
		}
	}
	line("チーム", o.TeamName)
	if o.Owner != nil {
		line("オーナー", o.Owner.Name)
	}
	if o.StartOn != "" || o.DueOn != "" {
		line("期間", strings.TrimSpace(o.StartOn+" 〜 "+o.DueOn))
	}
	line("色", o.Color)
	if o.Counts != nil {
		progress := fmt.Sprintf("%d / %d 件完了", o.Counts.CompletedTasks, o.Counts.Tasks)
		if o.Counts.Tasks > 0 {
			progress += fmt.Sprintf("（%d%%）", o.Counts.CompletedTasks*100/o.Counts.Tasks)
		}
		if o.Counts.Milestones > 0 {
			progress += fmt.Sprintf("、マイルストーン %d / %d", o.Counts.CompletedMilestones, o.Counts.Milestones)
		}
		line("進み具合", progress)
	}
	if o.Status != nil {
		line("最新のステータス", strings.TrimSpace("["+format.StatusLabel(o.Status.StatusType)+"] "+o.Status.Title+"  "+o.Status.CreatedAt))
	}
	if o.Pending {
		line("未送信", "説明などの変更がまだ Asana に送られていません")
	}
	line("URL", o.PermalinkURL)
	if notes := strings.TrimSpace(o.Notes); notes != "" {
		fmt.Fprintf(out, "\n説明:\n%s\n", indent(notes))
	}
	if o.Brief != nil {
		heading := "ブリーフ"
		if o.Brief.Title != "" {
			heading += "「" + o.Brief.Title + "」"
		}
		fmt.Fprintf(out, "\n%s:\n%s\n", heading, indent(strings.TrimSpace(o.Brief.Text)))
	}
}

func (a *app) projectEditCmd() *cobra.Command {
	var notes, owner, start, due, color string
	cmd := &cobra.Command{
		Use:   "edit <プロジェクト> [--notes 説明] [--owner ユーザー] [--start 日付] [--due 日付] [--color 色]",
		Short: "プロジェクトの説明・オーナー・開始日・期日・色を変える（オフラインでも積む）",
		Example: `  asagao project edit 新居 --owner me --due 2026-12-31
  asagao project edit 新居 --notes "引っ越しを **年内** に終える"
  asagao project edit 新居 --start "" --due ""   # 日付を外す`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			in := usecase.UpdateProjectInput{}
			for flag, v := range map[string]**string{"notes": &in.Notes, "owner": &in.Owner, "start": &in.StartOn, "due": &in.DueOn, "color": &in.Color} {
				if cmd.Flags().Changed(flag) {
					val, _ := cmd.Flags().GetString(flag)
					*v = &val
				}
			}
			if in == (usecase.UpdateProjectInput{}) {
				return errors.New("変える項目を --notes / --owner / --start / --due / --color で指定してください")
			}
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			projects := &usecase.Projects{Store: st, Text: richtext.Converter{}, Clock: time.Now}
			if _, _, err := projects.Update(ctx, args[0], in); err != nil {
				return flagErr(err, map[string]string{"owner": "owner", "start_on": "start", "due_on": "due", "color": "color"})
			}
			return a.reportWrite(cmd, st, writeResult{}, "プロジェクトを変更しました")
		},
	}
	cmd.Flags().StringVar(&notes, "notes", "", "説明（マークダウン。全体を置き換える）")
	cmd.Flags().StringVar(&owner, "owner", "", `オーナー（"me"・ユーザーの gid か名前。"none" か "" で外す）`)
	cmd.Flags().StringVar(&start, "start", "", `開始日（YYYY-MM-DD / today / tomorrow / +Nd。"" で外す。期日が必要）`)
	cmd.Flags().StringVar(&due, "due", "", `期日（YYYY-MM-DD / today / tomorrow / +Nd。"" で外す）`)
	cmd.Flags().StringVar(&color, "color", "", "色（"+strings.Join(project.Colors, " / ")+"。Asana が表示する新しい名前 "+strings.Join(project.NewColors(), " / ")+" も使える）")
	return a.addDryRunFlag(cmd)
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
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s  %s %s\n%s\n\n", format.StatusLabel(u.StatusType), u.Title, u.Author, u.CreatedAt, indent(u.Text))
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
	var team string
	cmd := &cobra.Command{
		Use:   "create <名前> [--team チーム]",
		Short: "プロジェクトを作る（オンラインのみ。チームが複数あれば --team で選ぶ。候補は project teams）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			name := strings.Join(args, " ")
			teamGID, err := query(st).TeamForNewProject(ctx, team)
			if err != nil {
				return fmt.Errorf("--team: %w", err)
			}
			if a.dryRun {
				planned := plannedChange{Kind: "create_project", Description: "プロジェクト「" + name + "」の作成"}
				if teamGID != "" {
					planned.Payload = map[string]any{"team": teamGID}
				}
				return a.dryRunPlan(cmd, planned)
			}
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			p, err := s.CreateProject(ctx, name, teamGID)
			if err != nil {
				return err
			}
			msg := fmt.Sprintf("プロジェクトを作りました: %s (%s)", p.Name, p.GID)
			if p.Team != nil && p.Team.Name != "" {
				msg += "  チーム: " + p.Team.Name // a partial --team names the team it picked
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		},
	}
	cmd.Flags().StringVar(&team, "team", "", "チーム（gid または名前。部分一致可）")
	return a.addDryRunFlag(cmd)
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
