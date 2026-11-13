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

	"github.com/amasok/asana-tui/internal/dates"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

func (a *app) taskCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "タスクの一覧・詳細・作成・完了"}
	cmd.AddCommand(a.taskListCmd(), a.taskShowCmd(), a.taskCreateCmd(),
		a.taskCompleteCmd("complete", true), a.taskCompleteCmd("reopen", false), a.taskMoveCmd(), a.taskAssignCmd(), a.taskTagCmd())
	return cmd
}

type listFlags struct {
	assignee, project, section, tag, dueBefore, dueAfter string
	completed, all, overdue                              bool
	limit, modifiedWithin                                int
}

func (f *listFlags) register(cmd *cobra.Command, defaultAssignee string) {
	cmd.Flags().StringVar(&f.assignee, "assignee", defaultAssignee, `担当者: "me"、"any"（全員）、またはユーザーの gid`)
	cmd.Flags().StringVar(&f.project, "project", "", "プロジェクト（gid または名前。部分一致可）")
	cmd.Flags().StringVar(&f.section, "section", "", "セクション（gid または名前。--project と併用）")
	cmd.Flags().StringVar(&f.tag, "tag", "", "タグ（名前または gid）")
	cmd.Flags().StringVar(&f.dueBefore, "due-before", "", "期日がこの日以前（YYYY-MM-DD / today / tomorrow / +Nd）")
	cmd.Flags().StringVar(&f.dueAfter, "due-after", "", "期日がこの日以降（YYYY-MM-DD / today / tomorrow / +Nd）")
	cmd.Flags().BoolVar(&f.overdue, "overdue", false, "期限切れの未完了タスク")
	cmd.Flags().BoolVar(&f.completed, "completed", false, "完了済みのタスクだけ")
	cmd.Flags().BoolVar(&f.all, "all", false, "完了・未完了の両方")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "最大件数（0 = 無制限）")
	cmd.Flags().IntVar(&f.modifiedWithin, "modified-within", 0, "直近 N 日に更新されたタスクを新しい順に（最近の更新）")
}

// filter converts flags to a store filter. The default assignee is "me" unless a project is given.
func (f *listFlags) filter(ctx context.Context, st *store.Store, cmd *cobra.Command, now time.Time) (store.TaskFilter, error) {
	var tf store.TaskFilter
	assignee := f.assignee
	if f.project != "" && !cmd.Flags().Changed("assignee") {
		assignee = "any"
	}
	switch assignee {
	case "any", "all", "":
	case "me":
		me, err := st.MeGID(ctx)
		if err != nil {
			return tf, err
		}
		tf.AssigneeGID = me
	default:
		tf.AssigneeGID = assignee
	}
	if f.project != "" {
		gid, err := st.ResolveProject(ctx, f.project)
		if err != nil {
			return tf, err
		}
		tf.ProjectGID = gid
		if f.section != "" {
			if tf.SectionGID, err = st.ResolveSection(ctx, gid, f.section); err != nil {
				return tf, err
			}
		}
	} else if f.section != "" {
		return tf, errors.New("--section は --project と一緒に指定してください")
	}
	tf.Tag = f.tag
	var err error
	if tf.DueBefore, err = parseDate(f.dueBefore, now); err != nil {
		return tf, fmt.Errorf("--due-before: %w", err)
	}
	if tf.DueAfter, err = parseDate(f.dueAfter, now); err != nil {
		return tf, fmt.Errorf("--due-after: %w", err)
	}
	if f.overdue {
		tf.DueBefore = now.AddDate(0, 0, -1).Format(time.DateOnly)
	}
	switch {
	case f.all:
	case f.completed && !f.overdue:
		v := true
		tf.Completed = &v
	default:
		v := false
		tf.Completed = &v
	}
	tf.Limit = f.limit
	if f.modifiedWithin > 0 {
		tf.ModifiedAfter = now.AddDate(0, 0, -f.modifiedWithin).UTC().Format(time.RFC3339)
		tf.SortByModified = true
		if !cmd.Flags().Changed("completed") && !cmd.Flags().Changed("all") {
			tf.Completed = nil
		}
	}
	return tf, nil
}

func (a *app) taskListCmd() *cobra.Command {
	var f listFlags
	cmd := &cobra.Command{
		Use:   "list",
		Short: "タスクを一覧表示する（ローカルストアから。デフォルトは自分の未完了タスク）",
		Example: `  asana-tui task list
  asana-tui task list --due-before +7d
  asana-tui task list --project 新居 --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			tf, err := f.filter(ctx, st, cmd, time.Now())
			if err != nil {
				return err
			}
			tasks, err := st.ListTasks(ctx, tf)
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
			tf, err := f.filter(ctx, st, cmd, time.Now())
			if err != nil {
				return err
			}
			if !cmd.Flags().Changed("completed") && !cmd.Flags().Changed("all") {
				tf.Completed = nil // search finds completed tasks too unless told otherwise
			}
			tf.Query = strings.Join(args, " ")
			tasks, err := st.ListTasks(ctx, tf)
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
		Use:   "show <gid>",
		Short: "タスクの詳細（サブタスク・コメント込み）を表示する",
		Long: "オンラインなら Asana から最新の詳細を取得してローカルストアを更新してから表示します。\n" +
			"取得できない場合や --offline のときはローカルストアの内容を表示します。",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
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
			d, err := st.GetTask(ctx, args[0])
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

func (a *app) fetchDetails(ctx context.Context, st *store.Store, gid string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	api, err := a.apiClient(ctx)
	if err != nil {
		return err
	}
	return (&sync.Syncer{API: api, Store: st, Cfg: a.cfg.Sync}).FetchDetails(ctx, gid)
}

// parseDate accepts YYYY-MM-DD, "today", "tomorrow", "yesterday" and "+Nd" / "-Nd".
func parseDate(s string, now time.Time) (string, error) { return dates.Parse(s, now) }

var weekdays = [...]string{"日", "月", "火", "水", "木", "金", "土"}

func formatDue(t store.TaskSummary) string {
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

func membershipLabel(ms []store.MembershipView) string {
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

func printTaskTable(w io.Writer, tasks []store.TaskSummary) {
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
		rows = append(rows, []string{checkbox(t.Completed), formatDue(t), t.Name, membershipLabel(t.Memberships), tags, t.GID})
	}
	printTable(w, rows)
	fmt.Fprintf(w, "\n%d 件\n", len(tasks))
}

func printTaskDetail(w io.Writer, d *store.TaskDetail) {
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
	var comments []store.StoryView
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
			fmt.Fprintf(w, "  %s  %s\n%s\n", who, when, indent(indent(c.Text)))
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
