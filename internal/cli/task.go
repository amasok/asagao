package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

func (a *app) taskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "タスクの一覧・詳細・作成・完了"}
	cmd.AddCommand(a.taskListCmd(), a.taskShowCmd(), a.taskCreateCmd(),
		a.taskCompleteCmd("complete", true), a.taskCompleteCmd("reopen", false), a.taskMoveCmd(), a.taskAssignCmd(), a.taskTagCmd())
	cmd.AddCommand(a.taskExtraCmds()...)
	return cmd
}

type listFlags struct {
	assignee, project, section, tag, dueBefore, dueAfter string
	field                                                string
	completed, all, overdue                              bool
	limit, modifiedWithin                                int
}

func (f *listFlags) register(cmd *cobra.Command, defaultAssignee string) {
	cmd.Flags().StringVar(&f.assignee, "assignee", defaultAssignee, `担当者: "me"、"any"（全員）、またはユーザーの gid`)
	cmd.Flags().StringVar(&f.project, "project", "", "プロジェクト（gid または名前。部分一致可）")
	cmd.Flags().StringVar(&f.section, "section", "", "セクション（gid または名前。--project と併用）")
	cmd.Flags().StringVar(&f.tag, "tag", "", "タグ（名前または gid）")
	cmd.Flags().StringVar(&f.field, "field", "", `カスタムフィールドの値: "優先度=高"（"優先度" だけなら値があるもの）`)
	cmd.Flags().StringVar(&f.dueBefore, "due-before", "", "期日がこの日以前（YYYY-MM-DD / today / tomorrow / +Nd）")
	cmd.Flags().StringVar(&f.dueAfter, "due-after", "", "期日がこの日以降（YYYY-MM-DD / today / tomorrow / +Nd）")
	cmd.Flags().BoolVar(&f.overdue, "overdue", false, "期限切れの未完了タスク")
	cmd.Flags().BoolVar(&f.completed, "completed", false, "完了済みのタスクだけ")
	cmd.Flags().BoolVar(&f.all, "all", false, "完了・未完了の両方")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "最大件数（0 = 無制限）")
	cmd.Flags().IntVar(&f.modifiedWithin, "modified-within", 0, "直近 N 日に更新されたタスクを新しい順に（最近の更新）")
}

// spec converts flags to a filter spec. The default assignee (defaultAssignee) applies when
// --assignee is not given; with --project it is everyone. defaultCompleted applies when
// neither --completed nor --all is given.
func (f *listFlags) spec(cmd *cobra.Command, defaultAssignee string, defaultCompleted *bool) readmodel.FilterSpec {
	sp := readmodel.FilterSpec{
		DefaultAssignee: defaultAssignee, DefaultCompleted: defaultCompleted,
		Project: f.project, Section: f.section, Tag: f.tag, Field: f.field,
		DueBefore: f.dueBefore, DueAfter: f.dueAfter, Overdue: f.overdue,
		ModifiedWithinDays: f.modifiedWithin, Limit: f.limit,
	}
	if cmd.Flags().Changed("assignee") {
		sp.Assignee = f.assignee
		if sp.Assignee == "" {
			sp.Assignee = "any"
		}
	}
	switch {
	case f.all:
		sp.Completion = "all"
	case f.completed:
		sp.Completion = "completed"
	}
	return sp
}

// filter builds the store filter for the flags.
func (f *listFlags) filter(ctx context.Context, st Store, cmd *cobra.Command, defaultAssignee string, defaultCompleted *bool, now time.Time) (readmodel.TaskFilter, error) {
	tf, err := query(st).BuildFilter(ctx, f.spec(cmd, defaultAssignee, defaultCompleted), now)
	var fe *readmodel.FilterError
	if errors.As(err, &fe) {
		flag := "--" + strings.ReplaceAll(fe.Field, "_", "-")
		if errors.Is(fe.Err, readmodel.ErrSectionWithoutProject) {
			return tf, errors.New("--section は --project と一緒に指定してください")
		}
		return tf, fmt.Errorf("%s: %w", flag, fe.Err)
	}
	return tf, err
}

func (a *app) taskListCmd() *cobra.Command {
	var f listFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "タスクを一覧表示する（ローカルストアから。デフォルトは自分の未完了タスク。.asana.json があればそのプロジェクトの未完了タスク）",
		Example: `  asagao task list
  asagao task list --due-before +7d
  asagao task list --project 新居 --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			if !cmd.Flags().Changed("project") {
				c, err := a.repoContext()
				if err != nil {
					return err
				}
				if c != nil && c.Project != "" {
					f.project = c.Project
					noteContext(cmd, c, "プロジェクトで絞っています（全体は --project \"\"）")
				}
			}
			open := false
			tf, err := f.filter(ctx, st, cmd, "me", &open, time.Now())
			if err != nil {
				return err
			}
			tasks, err := query(st).ListTasks(ctx, tf)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(tasks)
			}
			printTaskTable(cmd.OutOrStdout(), tasks)
			return nil
		},
	}
	f.register(cmd, "me")
	return cmd
}

func (a *app) searchCmd() *cobra.Command {
	var f listFlags
	cmd := &cobra.Command{
		Use:   "search <キーワード>...",
		Short: "タスク名・説明・コメントを全文検索する（ローカルストアから）",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			// search finds completed tasks too unless told otherwise
			tf, err := f.filter(ctx, st, cmd, "any", nil, time.Now())
			if err != nil {
				return err
			}
			tf.Query = strings.Join(args, " ")
			tasks, err := query(st).ListTasks(ctx, tf)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(tasks)
			}
			printTaskTable(cmd.OutOrStdout(), tasks)
			return nil
		},
	}
	f.register(cmd, "any")
	return cmd
}

func (a *app) taskShowCmd() *cobra.Command {
	var offline bool
	cmd := &cobra.Command{
		Use:   "show [<gid>]",
		Short: "タスクの詳細（サブタスク・コメント込み）を表示する",
		Long: "オンラインなら Asana から最新の詳細を取得してローカルストアを更新してから表示します。\n" +
			"取得できない場合や --offline のときはローカルストアの内容を表示します。\n" +
			"gid を省くと .asana.json のタスクを表示します。",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				c, err := a.repoContext()
				if err != nil {
					return err
				}
				if c == nil || c.Task == "" {
					return errors.New("gid を指定してください（.asana.json に task もありません）")
				}
				args = []string{c.Task}
			}
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()

			var fetchErr error
			if !offline {
				fetchErr = a.fetchDetails(ctx, st, args[0])
			}
			d, err := query(st).GetTask(ctx, args[0])
			if err != nil {
				if fetchErr != nil {
					return fmt.Errorf("%w（Asana からの取得にも失敗: %v）", err, fetchErr)
				}
				return err
			}
			var attErr *sync.AttachmentsError
			switch {
			case errors.As(fetchErr, &attErr):
				fmt.Fprintf(cmd.ErrOrStderr(), "注意: %v\n", attErr)
			case fetchErr != nil:
				fmt.Fprintf(cmd.ErrOrStderr(), "注意: Asana から取得できなかったため、ローカルストアの内容を表示します（%v）\n", fetchErr)
			}
			if a.json {
				return a.printJSON(d)
			}
			printTaskDetail(cmd.OutOrStdout(), d)
			return nil
		},
	}
	cmd.Flags().BoolVar(&offline, "offline", false, "Asana に問い合わせず、ローカルストアだけで表示する")
	return cmd
}

func (a *app) fetchDetails(ctx context.Context, st Store, gid string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	api, err := a.apiClient(ctx)
	if err != nil {
		return err
	}
	return a.syncer(api, st).FetchDetails(ctx, gid)
}

var weekdays = [...]string{"日", "月", "火", "水", "木", "金", "土"}

func formatDue(t readmodel.TaskSummary) string {
	if t.DueOn == "" {
		return ""
	}
	d, err := time.Parse(time.DateOnly, t.DueOn)
	if err != nil {
		return t.DueOn
	}
	s := fmt.Sprintf("%s(%s)", d.Format("01/02"), weekdays[d.Weekday()])
	if d.Year() != time.Now().Year() {
		s = d.Format("2006/") + s
	}
	return s
}

func checkbox(done bool) string {
	if done {
		return "☑"
	}
	return "☐"
}

func membershipLabel(ms []readmodel.MembershipView) string {
	parts := make([]string, 0, len(ms))
	for _, m := range ms {
		if m.SectionName != "" && m.SectionName != "(no section)" && m.SectionName != "Untitled section" {
			parts = append(parts, m.ProjectName+" / "+m.SectionName)
		} else {
			parts = append(parts, m.ProjectName)
		}
	}
	return strings.Join(parts, ", ")
}

func printTaskTable(w io.Writer, tasks []readmodel.TaskSummary) {
	if len(tasks) == 0 {
		fmt.Fprintln(w, "該当するタスクはありません")
		return
	}
	rows := make([][]string, 0, len(tasks))
	for _, t := range tasks {
		tags := ""
		if len(t.Tags) > 0 {
			tags = "#" + strings.Join(t.Tags, " #")
		}
		for _, f := range t.CustomFields {
			if f.Important && f.Display != "" {
				tags = strings.TrimSpace(tags + " " + f.Name + ":" + f.Display)
			}
		}
		name := t.Name
		if b := readmodel.TaskBadges(t); b != "" {
			name += " " + b
		}
		rows = append(rows, []string{checkbox(t.Completed), formatDue(t), name, membershipLabel(t.Memberships), tags, t.GID})
	}
	printTable(w, rows)
	fmt.Fprintf(w, "\n%d 件\n", len(tasks))
}

func printTaskDetail(w io.Writer, d *readmodel.TaskDetail) {
	fmt.Fprintf(w, "%s %s\n\n", checkbox(d.Completed), d.Name)
	row := func(label, v string) {
		if v != "" {
			fmt.Fprintf(w, "  %s  %s\n", runewidth.FillRight(label, 12), v)
		}
	}
	if d.Assignee != nil {
		row("担当", d.Assignee.Name)
	}
	row("期日", formatDue(d.TaskSummary))
	row("開始日", d.StartOn)
	row("プロジェクト", membershipLabel(d.Memberships))
	if len(d.Tags) > 0 {
		row("タグ", "#"+strings.Join(d.Tags, " #"))
	}
	if d.Parent != nil {
		row("親タスク", d.Parent.Name+" ("+d.Parent.GID+")")
	}
	if d.ResourceSubtype != "" && d.ResourceSubtype != "default_task" {
		row("種類", task.SubtypeLabel(d.ResourceSubtype))
	}
	if d.ResourceSubtype == "approval" {
		status := d.ApprovalStatus
		if status == "" {
			status = "pending"
		}
		row("承認", task.ApprovalLabel(status))
	}
	for _, list := range []struct {
		label string
		deps  []readmodel.DependencyView
	}{{"待っている", d.Dependencies}, {"待たれている", d.Dependents}} {
		for i, dep := range list.deps {
			label := ""
			if i == 0 {
				label = list.label
			}
			v := checkbox(dep.Completed) + " " + dep.Name + " (" + dep.GID + ")"
			if dep.Pending {
				v += " ↑"
			}
			row(label, v)
		}
	}
	for _, f := range d.CustomFields {
		v := f.Display
		if v == "" {
			v = "-"
		}
		row(f.Name, v)
	}
	row("URL", d.PermalinkURL)
	if d.Notes != "" {
		fmt.Fprintf(w, "\n%s\n", indent(d.Notes))
	}
	if len(d.Subtasks) > 0 {
		fmt.Fprintf(w, "\nサブタスク (%d)\n", len(d.Subtasks))
		for _, s := range d.Subtasks {
			fmt.Fprintf(w, "  %s %s  %s\n", checkbox(s.Completed), s.Name, formatDue(s))
		}
	}
	var comments []readmodel.StoryView
	for _, s := range d.Stories {
		if s.Type == "comment" {
			comments = append(comments, s)
		}
	}
	if len(comments) > 0 {
		fmt.Fprintf(w, "\nコメント (%d)\n", len(comments))
		for _, c := range comments {
			who := ""
			if c.CreatedBy != nil {
				who = c.CreatedBy.Name
			}
			when := c.CreatedAt
			if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil {
				when = t.Local().Format("2006-01-02 15:04")
			}
			pending := ""
			if c.Pending {
				pending = "  （未送信）"
			}
			fmt.Fprintf(w, "  %s  %s  [%s]%s\n%s\n", who, when, c.GID, pending, indent(indent(c.Text)))
			if r := readmodel.ReactionsLine(c.Reactions); r != "" {
				fmt.Fprintf(w, "      %s\n", r)
			}
		}
	}
	if len(d.Attachments) > 0 {
		fmt.Fprintf(w, "\n添付 (%d)\n", len(d.Attachments))
		for _, at := range d.Attachments {
			fmt.Fprintf(w, "  %s  [%s]\n", at.Name, at.GID)
		}
	}
	if d.DetailsFetchedAt == "" {
		fmt.Fprintln(w, "\n（コメントはまだ取得していません。オンラインで再度 show すると取得します）")
	}
}

// printTable aligns columns by display width (full-width characters count as 2),
// which text/tabwriter does not do.
func printTable(w io.Writer, rows [][]string) {
	var widths []int
	for _, r := range rows {
		for i, c := range r {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], runewidth.StringWidth(c))
		}
	}
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			if i == len(r)-1 {
				b.WriteString(c)
				break
			}
			b.WriteString(runewidth.FillRight(c, widths[i]))
			b.WriteString("  ")
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}
