package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/sync"
)

// Templates (#2): listing task and project templates, making a task or a project from one
// (online only, like creating a project), and duplicating a project (project duplicate).

func (a *app) templateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "template", Short: "テンプレートの一覧と、テンプレートからタスク・プロジェクトを作る（オンラインのみ）"}
	cmd.AddCommand(a.templateTasksCmd(), a.templateProjectsCmd(), a.templateNewTaskCmd(), a.templateNewProjectCmd())
	return cmd
}

func taskTemplateGID(t model.TaskTemplate) string        { return t.GID }
func taskTemplateName(t model.TaskTemplate) string       { return t.Name }
func projectTemplateGID(t model.ProjectTemplate) string  { return t.GID }
func projectTemplateName(t model.ProjectTemplate) string { return t.Name }

func (a *app) templateTasksCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tasks <プロジェクト>",
		Short: "プロジェクトのタスクテンプレートを一覧表示する（Asana から取る）",
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
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			list, err := s.TaskTemplates(ctx, gid)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(nonNil(list))
			}
			out := cmd.OutOrStdout()
			if len(list) == 0 {
				fmt.Fprintln(out, "このプロジェクトにはタスクテンプレートがありません")
			}
			for _, t := range list {
				fmt.Fprintf(out, "%s  (%s)\n", t.Name, t.GID)
			}
			return nil
		},
	}
}

func (a *app) templateProjectsCmd() *cobra.Command {
	var team string
	cmd := &cobra.Command{
		Use:   "projects [--team チーム]",
		Short: "プロジェクトテンプレートを一覧表示する（Asana から取る。求められる日付と役割も出す）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			teamGID := ""
			if team != "" {
				if teamGID, err = query(st).ResolveTeam(ctx, team); err != nil {
					return fmt.Errorf("--team: %w", err)
				}
			}
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			list, err := s.ProjectTemplates(ctx, teamGID)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(nonNil(list))
			}
			out := cmd.OutOrStdout()
			if len(list) == 0 {
				fmt.Fprintln(out, "プロジェクトテンプレートがありません")
			}
			for _, t := range list {
				line := fmt.Sprintf("%s  (%s)", t.Name, t.GID)
				if t.Team != nil && t.Team.Name != "" {
					line += "  チーム: " + t.Team.Name
				}
				fmt.Fprintln(out, line)
				for _, d := range t.RequestedDates {
					fmt.Fprintf(out, "  日付: %s  (%s)\n", d.Name, d.GID)
				}
				for _, r := range t.RequestedRoles {
					fmt.Fprintf(out, "  役割: %s  (%s)\n", r.Name, r.GID)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&team, "team", "", "このチームのテンプレートだけ（gid または名前。部分一致可）")
	return cmd
}

func (a *app) templateNewTaskCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "new-task <プロジェクト> <テンプレート> [--name 名前]",
		Short: "タスクテンプレートからタスクを作る（オンラインのみ。テンプレートは gid か名前。候補は template tasks）",
		Args:  cobra.ExactArgs(2),
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
				return a.dryRunPlan(cmd, plannedChange{Kind: "instantiate_task",
					Description: "タスクテンプレート「" + args[1] + "」からタスクを作る",
					Payload:     map[string]any{"project": gid, "template": args[1], "name": name}})
			}
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			list, err := s.TaskTemplates(ctx, gid)
			if err != nil {
				return err
			}
			tmpl, err := sync.PickTemplate(list, args[1], taskTemplateGID, taskTemplateName)
			if err != nil {
				return err
			}
			made, err := s.InstantiateTask(ctx, tmpl, name)
			return a.reportMade(cmd, "タスク", made, err)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "タスク名（省略するとテンプレートの名前）")
	return a.addDryRunFlag(cmd)
}

func (a *app) templateNewProjectCmd() *cobra.Command {
	var team string
	var public bool
	var dates, roles []string
	cmd := &cobra.Command{
		Use:   "new-project <テンプレート> <名前> [--team チーム] [--date 日付=YYYY-MM-DD]... [--role 役割=ユーザー]...",
		Short: "プロジェクトテンプレートからプロジェクトを作る（オンラインのみ。候補は template projects。--date を省いた日付は今日）",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			in := sync.ProjectInput{Name: strings.Join(args[1:], " "), Public: public}
			if team != "" {
				if in.TeamGID, err = query(st).ResolveTeam(ctx, team); err != nil {
					return fmt.Errorf("--team: %w", err)
				}
			}
			if in.Dates, err = keyValues("--date", dates); err != nil {
				return err
			}
			if in.Roles, err = keyValues("--role", roles); err != nil {
				return err
			}
			for k, v := range in.Roles {
				if in.Roles[k], err = query(st).ResolveUser(ctx, v); err != nil || in.Roles[k] == "" {
					return fmt.Errorf("--role %s: %w", k, orNoUser(err))
				}
			}
			if a.dryRun {
				return a.dryRunPlan(cmd, plannedChange{Kind: "instantiate_project",
					Description: "プロジェクトテンプレート「" + args[0] + "」からプロジェクト「" + in.Name + "」を作る",
					Payload:     map[string]any{"template": args[0], "team": in.TeamGID, "dates": in.Dates, "roles": in.Roles, "public": in.Public}})
			}
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			list, err := s.ProjectTemplates(ctx, "")
			if err != nil {
				return err
			}
			tmpl, err := sync.PickTemplate(list, args[0], projectTemplateGID, projectTemplateName)
			if err != nil {
				return err
			}
			made, err := s.InstantiateProject(ctx, tmpl, in)
			return a.reportMade(cmd, "プロジェクト", made, err)
		},
	}
	cmd.Flags().StringVar(&team, "team", "", "チーム（gid または名前。部分一致可。省略するとテンプレートのチーム）")
	cmd.Flags().BoolVar(&public, "public", false, "チームに公開する")
	cmd.Flags().StringArrayVar(&dates, "date", nil, "テンプレートが求める日付（名前か gid）=YYYY-MM-DD。省略した日付は今日")
	cmd.Flags().StringArrayVar(&roles, "role", nil, "テンプレートの役割（名前か gid）=ユーザー（me / 名前 / メール / gid）")
	return a.addDryRunFlag(cmd)
}

func (a *app) projectDuplicateCmd() *cobra.Command {
	var team string
	cmd := &cobra.Command{
		Use:   "duplicate <プロジェクト> [名前] [--team チーム]",
		Short: "プロジェクトを複製する（オンラインのみ。タスク・サブタスク・メンバー・日付などをコピー。名前の省略は「… のコピー」）",
		Args:  cobra.MinimumNArgs(1),
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
			name := strings.Join(args[1:], " ")
			if strings.TrimSpace(name) == "" {
				o, err := query(st).ProjectOverview(ctx, gid)
				if err != nil {
					return err
				}
				name = o.Name + " のコピー"
			}
			teamGID := ""
			if team != "" {
				if teamGID, err = query(st).ResolveTeam(ctx, team); err != nil {
					return fmt.Errorf("--team: %w", err)
				}
			}
			if a.dryRun {
				return a.dryRunPlan(cmd, plannedChange{Kind: "duplicate_project",
					Description: "プロジェクトを「" + name + "」として複製",
					Payload:     map[string]any{"project": gid, "name": name, "team": teamGID}})
			}
			s, err := a.onlineSyncer(ctx, st)
			if err != nil {
				return err
			}
			made, err := s.DuplicateProject(ctx, gid, name, teamGID)
			return a.reportMade(cmd, "プロジェクト", made, err)
		},
	}
	cmd.Flags().StringVar(&team, "team", "", "複製先のチーム（gid または名前。部分一致可。省略すると元と同じ）")
	return a.addDryRunFlag(cmd)
}

// reportMade prints what a template or a duplicate made (with --json, the sync.Made). A
// made thing that could not be stored is printed with the error.
func (a *app) reportMade(cmd *cobra.Command, what string, made *sync.Made, err error) error {
	if made == nil {
		return err
	}
	if a.json {
		if jerr := a.printJSON(made); jerr != nil {
			return jerr
		}
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%sを作りました: %s (%s)\n", what, made.Name, made.GID)
	if made.Pending {
		fmt.Fprintln(out, "  中身は Asana が作っている途中です。次の同期で取り込みます")
	}
	return err
}

// keyValues parses flag values of the form key=value.
func keyValues(flag string, vals []string) (map[string]string, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	out := map[string]string{}
	for _, kv := range vals {
		k, v, ok := strings.Cut(kv, "=")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("%s は 名前=値 の形で書いてください: %q", flag, kv)
		}
		if _, dup := out[k]; dup {
			return nil, fmt.Errorf("%s %s が 2 回あります", flag, k)
		}
		out[k] = v
	}
	return out, nil
}

func orNoUser(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("ユーザーを指定してください")
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
