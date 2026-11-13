package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

// writeResult is the JSON output of write commands.
type writeResult struct {
	TaskGID   string           `json:"task_gid,omitempty"`
	CommentID string           `json:"comment_gid,omitempty"`
	Sent      bool             `json:"sent"`
	Push      *sync.PushResult `json:"push,omitempty"`
	// Discarded lists queued changes dropped because what they changed was deleted in Asana
	// (by this push or an earlier one not shown yet).
	Discarded []store.Discard `json:"discarded,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// printDiscard prints a dropped change with what the user wrote in full, so it can be
// recovered (copied back into Asana).
func printDiscard(w io.Writer, d store.Discard) {
	when := ""
	if t, err := time.Parse(time.RFC3339, d.DiscardedAt); err == nil {
		when = "  (" + t.Local().Format("2006-01-02 15:04") + ")"
	}
	fmt.Fprintln(w, "  - "+store.DiscardLine(d)+when)
	for _, dt := range store.DiscardDetails(d) {
		fmt.Fprintf(w, "      %s:\n", dt.Label)
		for _, l := range strings.Split(strings.TrimRight(dt.Text, "\n"), "\n") {
			fmt.Fprintln(w, "        "+l)
		}
	}
}

// takeDiscards returns the dropped changes not shown yet and marks them as shown. When
// print is set they are printed to w first.
func takeDiscards(ctx context.Context, st *store.Store, w io.Writer, print bool) []store.Discard {
	ds, err := st.PendingDiscards(ctx)
	if err != nil || len(ds) == 0 {
		return nil
	}
	if print {
		fmt.Fprintln(w, store.DiscardNotice+"（Asana で削除されていたため）:")
		for _, d := range ds {
			printDiscard(w, d)
		}
		fmt.Fprintln(w, "  （あとで見るには `asana-tui outbox discarded`）")
	}
	ids := make([]int64, len(ds))
	for i, d := range ds {
		ids[i] = d.ID
	}
	_ = st.MarkDiscardsNotified(ctx, ids) // on failure they are shown again next time
	return ds
}

// pushNow tries to send the outbox right away. Failure is not an error for the command:
// the change is saved locally and will be sent by the next sync.
func (a *app) pushNow(ctx context.Context, st *store.Store) (*sync.PushResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	api, err := a.apiClient(ctx)
	if err != nil {
		return nil, err
	}
	res, err := (&sync.Syncer{API: api, Store: st, Cfg: a.cfg.Sync}).Push(ctx)
	return &res, err
}

func (a *app) reportWrite(cmd *cobra.Command, st *store.Store, r writeResult, done string) error {
	push, err := a.pushNow(cmd.Context(), st)
	r.Push = push
	if err != nil {
		r.Error = err.Error()
	} else {
		r.Sent = push.Remaining == 0 && push.Failed == 0 && push.Conflicts == 0
	}
	out := cmd.OutOrStdout()
	r.Discarded = takeDiscards(cmd.Context(), st, out, !a.json)
	dropped := false
	for _, d := range r.Discarded {
		if slices.Contains(strings.Split(r.TaskGID, ","), d.TaskGID) {
			dropped, r.Sent = true, false
		}
	}
	// The gid may have changed from local:... to the real one.
	if r.TaskGID != "" && store.IsLocalGID(r.TaskGID) && r.Sent {
		r.TaskGID = ""
	}
	if a.json {
		return a.printJSON(r)
	}
	switch {
	case dropped:
		fmt.Fprintf(out, "%s。ただし対象のタスクが Asana で削除されていたため、変更は破棄されました\n", done)
	case errors.Is(err, sync.ErrPushBusy):
		fmt.Fprintf(out, "%s（別の asana-tui が送信中です。この変更もそちらで送られます）\n", done)
	case err != nil:
		fmt.Fprintf(out, "%s（オフラインのため未送信。次の sync で送ります: %v）\n", done, err)
	case !r.Sent:
		fmt.Fprintf(out, "%s（送信できなかった変更があります。`asana-tui outbox list` で確認してください）\n", done)
	default:
		fmt.Fprintf(out, "%s（Asana に送信しました）\n", done)
	}
	return nil
}

func (a *app) taskCompleteCmd(use string, completed bool) *cobra.Command {
	short := "タスクを完了にする"
	if !completed {
		short = "タスクを未完了に戻す"
	}
	return &cobra.Command{
		Use:   use + " <gid>...",
		Short: short,
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			for _, gid := range args {
				if err := st.UpdateTaskLocal(ctx, gid, map[string]any{"completed": completed}, time.Now()); err != nil {
					return err
				}
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: strings.Join(args, ",")}, fmt.Sprintf("%d 件を更新しました", len(args)))
		},
	}
}

// createOpts are the flags of `task create`.
type createOpts struct {
	name, project, section, due, assignee, notes, parent string
}

// createTask resolves every option and then creates the task locally. Nothing is written
// when an option is invalid.
func createTask(ctx context.Context, st *store.Store, o createOpts, now time.Time) (string, error) {
	nt := store.NewTask{Name: o.name, ParentGID: o.parent}
	if o.notes != "" { // Markdown -> Asana rich text
		nt.Notes, nt.HTMLNotes = richtext.PlainFromMarkdown(o.notes), richtext.FromMarkdown(o.notes)
	}
	var err error
	if nt.DueOn, err = parseDate(o.due, now); err != nil {
		return "", fmt.Errorf("--due: %w", err)
	}
	if o.parent != "" {
		// subtask: workspace comes from the parent; no project/section
	} else if o.project != "" {
		if nt.ProjectGID, err = st.ResolveProject(ctx, o.project); err != nil {
			return "", err
		}
		if o.section != "" {
			nt.SectionGID, err = st.ResolveSection(ctx, nt.ProjectGID, o.section)
		} else {
			nt.SectionGID, err = st.FirstSection(ctx, nt.ProjectGID)
		}
		if err != nil {
			return "", err
		}
	} else if nt.WorkspaceGID, err = st.DefaultWorkspace(ctx); err != nil {
		return "", err
	}
	assignee := o.assignee
	if o.project == "" && o.parent == "" && assignee == "" {
		assignee = "me"
	}
	if nt.AssigneeGID, err = st.ResolveUser(ctx, assignee); err != nil {
		return "", fmt.Errorf("--assignee: %w", err)
	}
	return st.CreateTaskLocal(ctx, nt, now)
}

func (a *app) taskCreateCmd() *cobra.Command {
	var o createOpts
	cmd := &cobra.Command{
		Use:   "create <タスク名>",
		Short: "タスクを作成する（プロジェクト未指定なら自分に割り当て）",
		Example: `  asana-tui task create "牛乳を買う" --due tomorrow
  asana-tui task create "ソファを買う" --project 新居 --section 家具`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			o.name = strings.Join(args, " ")
			gid, err := createTask(ctx, st, o, time.Now())
			if err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: gid}, "タスクを作成しました: "+o.name)
		},
	}
	cmd.Flags().StringVar(&o.project, "project", "", "プロジェクト（gid または名前）")
	cmd.Flags().StringVar(&o.section, "section", "", "セクション（gid または名前。省略時は先頭のセクション）")
	cmd.Flags().StringVar(&o.due, "due", "", "期日（YYYY-MM-DD / today / tomorrow / +Nd）")
	cmd.Flags().StringVar(&o.assignee, "assignee", "", `担当者（"me"・"none"・ユーザーの gid か名前。プロジェクト未指定なら me）`)
	cmd.Flags().StringVar(&o.notes, "notes", "", "説明（マークダウン）")
	cmd.Flags().StringVar(&o.parent, "parent", "", "親タスクの gid（サブタスクとして作成）")
	return cmd
}

func (a *app) taskTagCmd() *cobra.Command {
	var add, remove []string
	cmd := &cobra.Command{
		Use:     "tag <gid> [--add タグ]... [--remove タグ]...",
		Short:   "タグを付ける / 外す（ないタグは作る）",
		Example: `  asana-tui task tag 1203455468597469 --add 急ぎ --remove 買い物`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			edit, err := st.ResolveTagEdit(ctx, add, remove)
			if err != nil {
				return err
			}
			if err := st.ApplyTagEdit(ctx, args[0], edit, time.Now()); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, "タグを変更しました")
		},
	}
	cmd.Flags().StringSliceVar(&add, "add", nil, "付けるタグ（名前または gid）")
	cmd.Flags().StringSliceVar(&remove, "remove", nil, "外すタグ（名前または gid）")
	return cmd
}

// withStore opens the store for a write command and reports the result.
func (a *app) writeCommand(use, short string, args cobra.PositionalArgs, run func(cmd *cobra.Command, st *store.Store, args []string) (string, string, error)) *cobra.Command {
	return &cobra.Command{
		Use: use, Short: short, Args: args,
		RunE: func(cmd *cobra.Command, argv []string) error {
			st, err := a.openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			gid, msg, err := run(cmd, st, argv)
			if err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: gid}, msg)
		},
	}
}

func (a *app) taskExtraCmds() []*cobra.Command {
	like := a.writeCommand("like <gid>", "いいねする（--off で取り消し）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st *store.Store, args []string) (string, string, error) {
			off, _ := cmd.Flags().GetBool("off")
			return args[0], "いいねを変更しました", st.UpdateTaskLocal(cmd.Context(), args[0], map[string]any{"liked": !off}, time.Now())
		})
	like.Flags().Bool("off", false, "いいねを取り消す")

	follow := a.writeCommand("follow <gid> [--add ユーザー]... [--remove ユーザー]...", "フォロワーを追加 / 外す（me・名前・gid）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st *store.Store, args []string) (string, string, error) {
			// resolve every name before changing anything
			addNames, _ := cmd.Flags().GetStringSlice("add")
			removeNames, _ := cmd.Flags().GetStringSlice("remove")
			add, err := st.ResolveUsers(cmd.Context(), addNames)
			if err != nil {
				return "", "", fmt.Errorf("--add: %w", err)
			}
			remove, err := st.ResolveUsers(cmd.Context(), removeNames)
			if err != nil {
				return "", "", fmt.Errorf("--remove: %w", err)
			}
			for _, f := range []struct {
				users  []string
				follow bool
			}{{add, true}, {remove, false}} {
				for _, gid := range f.users {
					if err := st.SetFollowerLocal(cmd.Context(), args[0], gid, f.follow, time.Now()); err != nil {
						return "", "", err
					}
				}
			}
			return args[0], "フォロワーを変更しました", nil
		})
	follow.Flags().StringSlice("add", nil, "追加するユーザー")
	follow.Flags().StringSlice("remove", nil, "外すユーザー")

	project := a.writeCommand("project <gid> [--add プロジェクト]... [--remove プロジェクト]...", "ほかのプロジェクトに追加 / 外す", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st *store.Store, args []string) (string, string, error) {
			addNames, _ := cmd.Flags().GetStringSlice("add")
			removeNames, _ := cmd.Flags().GetStringSlice("remove")
			add, err := st.ResolveProjects(cmd.Context(), addNames)
			if err != nil {
				return "", "", fmt.Errorf("--add: %w", err)
			}
			remove, err := st.ResolveProjects(cmd.Context(), removeNames)
			if err != nil {
				return "", "", fmt.Errorf("--remove: %w", err)
			}
			for _, p := range []struct {
				projects []string
				add      bool
			}{{add, true}, {remove, false}} {
				for _, gid := range p.projects {
					if err := st.SetProjectLocal(cmd.Context(), args[0], gid, "", p.add, time.Now()); err != nil {
						return "", "", err
					}
				}
			}
			return args[0], "プロジェクトを変更しました", nil
		})
	project.Flags().StringSlice("add", nil, "追加するプロジェクト（名前または gid）")
	project.Flags().StringSlice("remove", nil, "外すプロジェクト")

	duplicate := a.writeCommand("duplicate <gid>", "タスクを複製する（サブタスク・添付・フォロワーも）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st *store.Store, args []string) (string, string, error) {
			name, _ := cmd.Flags().GetString("name")
			gid, err := st.DuplicateTaskLocal(cmd.Context(), args[0], name, time.Now())
			return gid, "複製しました", err
		})
	duplicate.Flags().String("name", "", "複製したタスクの名前（省略時は「… のコピー」）")

	attach := a.writeCommand("attach <gid> <ファイル>", "ファイルを添付する（100MB まで）", cobra.ExactArgs(2),
		func(cmd *cobra.Command, st *store.Store, args []string) (string, string, error) {
			_, err := st.UploadAttachmentLocal(cmd.Context(), args[0], args[1], filepath.Join(config.CacheDir(), "attachments"), time.Now())
			return args[0], "添付しました: " + filepath.Base(args[1]), err
		})
	return []*cobra.Command{like, follow, project, duplicate, attach}
}

func (a *app) taskAssignCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "assign <gid> <me|none|ユーザーの gid か名前>",
		Short: "担当者を変更する（none で未割り当て）",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			gid, err := st.ResolveUser(ctx, args[1])
			if err != nil {
				return err
			}
			if err := st.UpdateTaskLocal(ctx, args[0], map[string]any{"assignee": gid}, time.Now()); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, "担当者を変更しました")
		},
	}
}

func (a *app) taskMoveCmd() *cobra.Command {
	var project, section string
	cmd := &cobra.Command{
		Use:     "move <gid> --section <セクション>",
		Short:   "タスクを別のセクションに移動する",
		Example: `  asana-tui task move 1203455468597469 --section Done`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			projectGID, sectionGID, err := st.ResolveMove(ctx, args[0], project, section)
			if err != nil {
				return err
			}
			if err := st.MoveTaskLocal(ctx, args[0], projectGID, sectionGID, time.Now()); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, "移動しました: → "+section)
		},
	}
	cmd.Flags().StringVar(&section, "section", "", "移動先のセクション（gid または名前）")
	cmd.Flags().StringVar(&project, "project", "", "プロジェクト（タスクが複数のプロジェクトに入っている場合）")
	_ = cmd.MarkFlagRequired("section")
	return cmd
}

func (a *app) sectionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "section", Short: "セクション（リスト）"}
	var project string
	rename := &cobra.Command{
		Use:     "rename <セクション> <新しい名前> --project <プロジェクト>",
		Short:   "セクションの名前を変更する",
		Example: `  asana-tui section rename "In Pregoress" "In Progress" --project 新居`,
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			projectGID, err := st.ResolveProject(ctx, project)
			if err != nil {
				return err
			}
			gid, err := st.ResolveSection(ctx, projectGID, args[0])
			if err != nil {
				return err
			}
			name := strings.Join(args[1:], " ")
			if err := st.RenameSectionLocal(ctx, gid, name, time.Now()); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "セクション名を変更しました: "+name)
		},
	}
	rename.Flags().StringVar(&project, "project", "", "プロジェクト（gid または名前）")
	_ = rename.MarkFlagRequired("project")
	cmd.AddCommand(rename)
	return cmd
}

func (a *app) tagCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "tag", Short: "タグ"}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "タグと、そのタグが付いたタスクの数を表示する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			tags, err := st.ListTags(cmd.Context())
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(tags)
			}
			rows := [][]string{}
			for _, t := range tags {
				rows = append(rows, []string{"#" + t.Name, fmt.Sprintf("未完了 %d / 全 %d", t.Open, t.Total)})
			}
			printTable(cmd.OutOrStdout(), rows)
			return nil
		},
	})
	return cmd
}

func (a *app) commentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "comment", Short: "コメント"}
	cmd.AddCommand(&cobra.Command{
		Use:   "add <タスクの gid> <コメント>",
		Short: "タスクにコメントする（マークダウン可）",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			me, err := st.Me(ctx)
			if err != nil {
				return err
			}
			text := strings.Join(args[1:], " ")
			gid, err := st.AddRichCommentLocal(ctx, args[0], richtext.PlainFromMarkdown(text), richtext.FromMarkdown(text), me, time.Now())
			if err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0], CommentID: gid}, "コメントしました")
		},
	})
	return cmd
}

func (a *app) outboxCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "outbox", Short: "未送信の変更（オフライン中の編集・競合）を確認・解決する"}

	withOp := func(use, short string, action func() (sync.ResolveAction, error)) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return fmt.Errorf("id は数値で指定してください: %q", args[0])
				}
				act, err := action()
				if err != nil {
					return err
				}
				ctx := cmd.Context()
				st, err := a.openStore(ctx)
				if err != nil {
					return err
				}
				defer st.Close()
				msg, err := a.resolve(ctx, st, cmd.OutOrStdout(), id, act)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), msg)
				return nil
			},
		}
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "未送信の変更を一覧表示する",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			ops, err := st.ListOps(cmd.Context())
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(ops)
			}
			takeDiscards(cmd.Context(), st, cmd.OutOrStdout(), true)
			if len(ops) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "未送信の変更はありません")
				return nil
			}
			for _, op := range ops {
				fmt.Fprintf(cmd.OutOrStdout(), "#%d  [%s]  %s\n", op.ID, store.OpLabel(op), store.DescribeOp(op))
				for k, v := range op.Conflict {
					fmt.Fprintf(cmd.OutOrStdout(), "      %s: こちら=%v / Asana=%v\n", store.FieldLabel(k), op.Payload[k], v)
				}
				if heading, text := store.OpNote(op); text != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "      %s: %s\n", heading, text)
				}
			}
			return nil
		},
	})

	var keep string
	resolve := withOp("resolve", "競合を解決する（--keep local: こちらの値で上書き / remote: Asana の値を採用）",
		func() (sync.ResolveAction, error) {
			switch keep {
			case "local":
				return sync.KeepLocal, nil
			case "remote":
				return sync.KeepRemote, nil
			}
			return "", fmt.Errorf(`--keep は "local" か "remote" を指定してください`)
		})
	resolve.Flags().StringVar(&keep, "keep", "", "local または remote")
	_ = resolve.MarkFlagRequired("keep")

	var limit int
	discarded := &cobra.Command{
		Use:   "discarded",
		Short: "Asana で削除されたものへの変更として破棄した変更を、書いた内容の全文つきで表示する（新しい順）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			ds, err := st.RecentDiscards(cmd.Context(), limit)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(ds)
			}
			if len(ds) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "破棄した変更はありません")
				return nil
			}
			for _, d := range ds {
				printDiscard(cmd.OutOrStdout(), d)
			}
			return nil
		},
	}
	discarded.Flags().IntVar(&limit, "limit", 20, "表示する件数")
	cmd.AddCommand(discarded)

	fixed := func(act sync.ResolveAction) func() (sync.ResolveAction, error) {
		return func() (sync.ResolveAction, error) { return act, nil }
	}
	cmd.AddCommand(resolve,
		withOp("retry", "送信に失敗した変更をもう一度送る", fixed(sync.Retry)),
		withOp("discard", "未送信の変更を取り消す", fixed(sync.Discard)),
	)
	return cmd
}

// resolve resolves an outbox entry (sync.Resolve), then sends it right away or refreshes
// the task from Asana as needed. Being offline is not an error: the next sync does it.
func (a *app) resolve(ctx context.Context, st *store.Store, out io.Writer, id int64, act sync.ResolveAction) (string, error) {
	r, err := sync.Resolve(ctx, st, id, act)
	if err != nil {
		return "", err
	}
	if r.Push {
		push, err := a.pushNow(ctx, st)
		if ds := takeDiscards(ctx, st, out, true); len(ds) > 0 {
			for _, d := range ds {
				if d.TaskGID == r.Op.TaskGID {
					return fmt.Sprintf("#%d: 対象が Asana で削除されていたため、破棄しました", id), nil
				}
			}
		}
		switch {
		case errors.Is(err, sync.ErrPushBusy):
			return fmt.Sprintf("#%d: 別の asana-tui が送信中です。そちらで送られます", id), nil
		case err != nil:
			return fmt.Sprintf("#%d: 次の sync で送ります（オフライン: %v）", id, err), nil
		case push.Remaining > 0 || push.Failed > 0 || push.Conflicts > 0:
			return fmt.Sprintf("#%d: 送信できなかった変更があります。`asana-tui outbox list` で確認してください", id), nil
		case act == sync.KeepLocal:
			return fmt.Sprintf("#%d: こちらの値で上書きしました", id), nil
		}
		return fmt.Sprintf("#%d: 送信しました", id), nil
	}
	if r.RefreshGID != "" {
		if err := a.fetchDetails(ctx, st, r.RefreshGID); err != nil {
			return fmt.Sprintf("#%d を取り消しました（Asana の最新値は次の sync で反映されます）", id), nil
		}
	}
	return fmt.Sprintf("#%d を取り消しました", id), nil
}
