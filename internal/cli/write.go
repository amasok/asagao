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

	usecase "github.com/amasok/asagao/internal/app" // the cli package has its own type app
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/domain/task"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/sync"
)

// writeResult is the JSON output of write commands.
type writeResult struct {
	TaskGID   string `json:"task_gid,omitempty"`
	CommentID string `json:"comment_gid,omitempty"`
	// SectionGID is the section `section create` made (its Asana gid once sent).
	SectionGID string           `json:"section_gid,omitempty"`
	Sent       bool             `json:"sent"`
	Push       *sync.PushResult `json:"push,omitempty"`
	// Discarded lists queued changes dropped because what they changed was deleted in Asana
	// (by this push or an earlier one not shown yet).
	Discarded []readmodel.Discard `json:"discarded,omitempty"`
	Error     string              `json:"error,omitempty"`
	// Quick is what `task create --quick` read from the name.
	Quick *quickAddJSON `json:"quick,omitempty"`
}

// printDiscard prints a dropped change with what the user wrote in full, so it can be
// recovered (copied back into Asana).
func printDiscard(w io.Writer, d readmodel.Discard) {
	when := ""
	if t, err := time.Parse(time.RFC3339, d.DiscardedAt); err == nil {
		when = "  (" + t.Local().Format("2006-01-02 15:04") + ")"
	}
	fmt.Fprintln(w, "  - "+readmodel.DiscardLine(d)+when)
	for _, dt := range readmodel.DiscardDetails(d, richtext.ToEditableMarkdown) {
		fmt.Fprintf(w, "      %s:\n", dt.Label)
		for _, l := range strings.Split(strings.TrimRight(dt.Text, "\n"), "\n") {
			fmt.Fprintln(w, "        "+l)
		}
	}
}

// takeDiscards returns the dropped changes not shown yet and marks them as shown. When
// print is set they are printed to w first.
func takeDiscards(ctx context.Context, st Store, w io.Writer, print bool) []readmodel.Discard {
	ds, err := query(st).PendingDiscards(ctx)
	if err != nil || len(ds) == 0 {
		return nil
	}
	var announce []readmodel.Discard // changes deleted by a reset were confirmed by the user
	for _, d := range ds {
		if !readmodel.IsResetDiscard(d) {
			announce = append(announce, d)
		}
	}
	if print && len(announce) > 0 {
		fmt.Fprintln(w, readmodel.DiscardNotice+"（Asana で削除されていたため）:")
		for _, d := range announce {
			printDiscard(w, d)
		}
		fmt.Fprintln(w, "  （あとで見るには `asagao outbox discarded`）")
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
func (a *app) pushNow(ctx context.Context, st Store) (*sync.PushResult, error) {
	if a.dryRun {
		return nil, errDryRunSend
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	api, err := a.apiClient(ctx)
	if err != nil {
		return nil, err
	}
	res, err := a.syncer(api, st).Push(ctx)
	return &res, err
}

func (a *app) reportWrite(cmd *cobra.Command, st Store, r writeResult, done string) error {
	if d, ok := st.(*dryRunStore); ok {
		return a.reportDryRun(cmd, d, r)
	}
	push, err := a.pushNow(cmd.Context(), st)
	r.Push = push
	if err != nil {
		r.Error = err.Error()
	} else {
		r.Sent = push.Remaining == 0 && push.Failed == 0 && push.Conflicts == 0 && !push.InboxReadOnly
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
	if r.TaskGID != "" && localgid.IsLocal(r.TaskGID) && r.Sent {
		r.TaskGID = ""
	}
	if localgid.IsLocal(r.SectionGID) {
		if real, err := st.RealSectionGID(cmd.Context(), r.SectionGID); err == nil {
			r.SectionGID = real
		}
	}
	if a.json {
		return a.printJSON(r)
	}
	switch {
	case push != nil && push.InboxReadOnly:
		fmt.Fprintf(out, "%s。ただし Asana が受け付けなかったため、元に戻しました\n%s\n", done, sync.InboxReadOnlyMessage(a.cfg.Auth.Scopes))
	case errors.Is(err, sync.ErrAccountChanged):
		fmt.Fprintf(out, "%s（まだ送っていません）\n%v\n", done, accountChanged(cmd.Context(), st))
	case dropped:
		fmt.Fprintf(out, "%s。ただし対象のタスクが Asana で削除されていたため、変更は破棄されました\n", done)
	case errors.Is(err, sync.ErrPushBusy):
		fmt.Fprintf(out, "%s（別の asagao が送信中です。この変更もそちらで送られます）\n", done)
	case err != nil:
		fmt.Fprintf(out, "%s（オフラインのため未送信。次の sync で送ります: %v）\n", done, err)
	case !r.Sent:
		fmt.Fprintf(out, "%s（送信できなかった変更があります。`asagao outbox list` で確認してください）\n", done)
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
	return a.addDryRunFlag(&cobra.Command{
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
			tasks := taskUseCases(st)
			for _, gid := range args {
				if err := tasks.UpdateHere(ctx, gid, usecase.TaskUpdate{Completed: &completed}); err != nil {
					return err
				}
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: strings.Join(args, ",")}, fmt.Sprintf("%d 件を更新しました", len(args)))
		},
	})
}

// createOpts are the flags of `task create`.
type createOpts struct {
	name, project, section, due, assignee, notes, parent string
	tags                                                 []string
}

// createTask resolves every option and then creates the task locally. Nothing is written
// when an option is invalid.
func createTask(ctx context.Context, st Store, o createOpts, now time.Time) (string, error) {
	tasks := usecase.Tasks{Store: st, Text: richtext.Converter{}, Clock: func() time.Time { return now }}
	gid, err := tasks.Create(ctx, usecase.CreateTaskInput{
		Name: o.name, Parent: o.parent, Project: o.project, Section: o.section,
		Due: o.due, Assignee: o.assignee, Notes: o.notes, Tags: o.tags,
	})
	var fe *usecase.FieldError
	if errors.As(err, &fe) {
		return "", fmt.Errorf("--%s: %w", fe.Field, fe.Err) // the field names are the flag names
	}
	return gid, err
}

// query reads the local cache (app.Query over the store).
func query(st Store) usecase.Query { return usecase.Query{Store: st} }

// taskUseCases is the task use cases on st, reading the clock at each write.
func taskUseCases(st Store) *usecase.Tasks {
	return &usecase.Tasks{Store: st, Text: richtext.Converter{}, Clock: time.Now}
}

// fieldErr returns the error inside a *usecase.FieldError as it is: a positional argument
// has no flag name to put before it.
func fieldErr(err error) error {
	var fe *usecase.FieldError
	if errors.As(err, &fe) {
		return fe.Err
	}
	return err
}

// relationUseCases is the use cases on a task's relations on st, reading the clock at
// each write.
func relationUseCases(st Store) *usecase.Relations {
	return &usecase.Relations{Store: st, Clock: time.Now}
}

// flagErr puts the command's flag before the error inside a *usecase.FieldError, as
// "--add: …"; flags maps the use case's field names to the flags.
func flagErr(err error, flags map[string]string) error {
	var fe *usecase.FieldError
	if errors.As(err, &fe) {
		if f, ok := flags[fe.Field]; ok {
			return fmt.Errorf("--%s: %w", f, fe.Err)
		}
	}
	return err
}

func (a *app) taskCreateCmd() *cobra.Command {
	var o createOpts
	var quick bool
	cmd := &cobra.Command{
		Use:   "create <タスク名>",
		Short: "タスクを作成する（プロジェクト未指定なら .asana.json のプロジェクト、それもなければ自分に割り当て）",
		Example: `  asagao task create "牛乳を買う" --due tomorrow
  asagao task create "ソファを買う" --project 新居 --section 家具
  asagao task create --quick "牛乳を買う @me 明日 #買い物"`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			o.name = strings.Join(args, " ")
			if !cmd.Flags().Changed("project") && o.parent == "" {
				c, err := a.repoContext()
				if err != nil {
					return err
				}
				if c != nil && c.Project != "" {
					o.project = c.Project
					if !cmd.Flags().Changed("section") {
						o.section = c.Section
					}
					noteContext(cmd, c, "プロジェクトに作ります")
				}
			}
			now := time.Now()
			var q *usecase.QuickAddResult
			if quick {
				if q, err = readQuickAdd(ctx, st, &o, now); err != nil {
					return err
				}
			}
			gid, err := createTask(ctx, st, o, now)
			if err != nil {
				return err
			}
			r := writeResult{TaskGID: gid}
			if q != nil {
				r.Quick = newQuickAddJSON(*q)
				if !a.json {
					printQuickAdd(cmd.OutOrStdout(), *q)
				}
			}
			return a.reportWrite(cmd, st, r, "タスクを作成しました: "+o.name)
		},
	}
	cmd.Flags().StringVar(&o.project, "project", "", "プロジェクト（gid または名前）")
	cmd.Flags().StringVar(&o.section, "section", "", "セクション（gid または名前。省略時は先頭のセクション）")
	cmd.Flags().StringVar(&o.due, "due", "", "期日（YYYY-MM-DD / today / tomorrow / +Nd）")
	cmd.Flags().StringVar(&o.assignee, "assignee", "", `担当者（"me"・"none"・ユーザーの gid か名前。プロジェクト未指定なら me）`)
	cmd.Flags().StringVar(&o.notes, "notes", "", "説明（マークダウン）")
	cmd.Flags().StringVar(&o.parent, "parent", "", "親タスクの gid（サブタスクとして作成）")
	cmd.Flags().BoolVar(&quick, "quick", false, "名前の末尾の @担当・期日・#タグ を読む（例: 牛乳を買う @me 明日 #買い物）")
	return a.addDryRunFlag(cmd)
}

func (a *app) taskTagCmd() *cobra.Command {
	var add, remove []string
	cmd := &cobra.Command{
		Use:     "tag <gid> [--add タグ]... [--remove タグ]...",
		Short:   "タグを付ける / 外す（ないタグは作る）",
		Example: `  asagao task tag 1203455468597469 --add 急ぎ --remove 買い物`,
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			relations := relationUseCases(st)
			edit, err := relations.Resolve(ctx, args[0], usecase.RelationsInput{AddTags: add, RemoveTags: remove})
			if err != nil {
				return err
			}
			if err := relations.Apply(ctx, args[0], edit, nil); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, "タグを変更しました")
		},
	}
	cmd.Flags().StringSliceVar(&add, "add", nil, "付けるタグ（名前または gid）")
	cmd.Flags().StringSliceVar(&remove, "remove", nil, "外すタグ（名前または gid）")
	return a.addDryRunFlag(cmd)
}

// withStore opens the store for a write command and reports the result.
func (a *app) writeCommand(use, short string, args cobra.PositionalArgs, run func(cmd *cobra.Command, st Store, args []string) (string, string, error)) *cobra.Command {
	return a.addDryRunFlag(&cobra.Command{
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
	})
}

func (a *app) taskExtraCmds() []*cobra.Command {
	like := a.writeCommand("like <gid>", "いいねする（--off で取り消し）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			off, _ := cmd.Flags().GetBool("off")
			liked := !off
			tasks := taskUseCases(st)
			return args[0], "いいねを変更しました", tasks.UpdateHere(cmd.Context(), args[0], usecase.TaskUpdate{Liked: &liked})
		})
	like.Flags().Bool("off", false, "いいねを取り消す")

	follow := a.writeCommand("follow <gid> [--add ユーザー]... [--remove ユーザー]...", "フォロワーを追加 / 外す（me・名前・gid）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			// resolve every name before changing anything
			addNames, _ := cmd.Flags().GetStringSlice("add")
			removeNames, _ := cmd.Flags().GetStringSlice("remove")
			relations := relationUseCases(st)
			edit, err := relations.Resolve(cmd.Context(), args[0], usecase.RelationsInput{AddFollowers: addNames, RemoveFollowers: removeNames})
			if err != nil {
				return "", "", flagErr(err, map[string]string{"add_followers": "add", "remove_followers": "remove"})
			}
			return args[0], "フォロワーを変更しました", relations.Apply(cmd.Context(), args[0], edit, nil)
		})
	follow.Flags().StringSlice("add", nil, "追加するユーザー")
	follow.Flags().StringSlice("remove", nil, "外すユーザー")

	project := a.writeCommand("project <gid> [--add プロジェクト]... [--remove プロジェクト]...", "ほかのプロジェクトに追加 / 外す", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			addNames, _ := cmd.Flags().GetStringSlice("add")
			removeNames, _ := cmd.Flags().GetStringSlice("remove")
			relations := relationUseCases(st)
			edit, err := relations.Resolve(cmd.Context(), args[0], usecase.RelationsInput{AddProjects: addNames, RemoveProjects: removeNames})
			if err != nil {
				return "", "", flagErr(err, map[string]string{"add_projects": "add", "remove_projects": "remove"})
			}
			return args[0], "プロジェクトを変更しました", relations.Apply(cmd.Context(), args[0], edit, nil)
		})
	project.Flags().StringSlice("add", nil, "追加するプロジェクト（名前または gid）")
	project.Flags().StringSlice("remove", nil, "外すプロジェクト")

	duplicate := a.writeCommand("duplicate <gid>", "タスクを複製する（サブタスク・添付・フォロワーも）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			name, _ := cmd.Flags().GetString("name")
			copies := &usecase.Copies{Store: st, Clock: time.Now}
			gid, err := copies.Duplicate(cmd.Context(), args[0], name)
			return gid, "複製しました", err
		})
	duplicate.Flags().String("name", "", "複製したタスクの名前（省略時は「… のコピー」）")

	attach := a.writeCommand("attach <gid> <ファイル>", "ファイルを添付する（100MB まで）", cobra.ExactArgs(2),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			copies := &usecase.Copies{Store: st, CacheDir: attachmentCacheDir(st), Clock: time.Now}
			_, err := copies.Attach(cmd.Context(), args[0], args[1])
			return args[0], "添付しました: " + filepath.Base(args[1]), err
		})
	field := a.writeCommand("field <gid> <フィールド> [値]", "カスタムフィールドを設定する（値なし / --clear で空に）", cobra.RangeArgs(2, 3),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			ctx := cmd.Context()
			f, err := query(st).ResolveCustomField(ctx, args[0], args[1])
			if err != nil {
				return "", "", err
			}
			var input any
			if len(args) == 3 {
				input = args[2]
			}
			if clear, _ := cmd.Flags().GetBool("clear"); clear {
				input = nil
			} else if input == nil {
				return "", "", fmt.Errorf("値を指定してください（空にするなら --clear）")
			}
			relations := relationUseCases(st)
			value, err := relations.ParseFieldValue(ctx, f.GID, input)
			if err != nil {
				return "", "", err
			}
			display, err := relations.SetFieldHere(ctx, args[0], f.GID, value)
			if err != nil {
				return "", "", err
			}
			if display == "" {
				return args[0], fmt.Sprintf("「%s」を空にしました", f.Name), nil
			}
			return args[0], fmt.Sprintf("「%s」を「%s」にしました", f.Name, display), nil
		})
	field.Flags().Bool("clear", false, "値を空にする")
	field.Example = `  asagao task field 1203455468597469 優先度 高
  asagao task field 1203455468597469 見積もり 3.5
  asagao task field 1203455468597469 ラベル "バグ,UI"     # 複数選択はカンマ区切り
  asagao task field 1203455468597469 締切 +7d
  asagao task field 1203455468597469 優先度 --clear`
	depend := a.writeCommand("depend <gid> [--add 待つタスク]... [--remove 待つタスク]... [--add-dependent タスク]... [--remove-dependent タスク]...",
		"依存関係を追加 / 解除する（待つタスク・待たれるタスクを gid で）", cobra.ExactArgs(1),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			ctx := cmd.Context()
			gids := func(flag string) []string { v, _ := cmd.Flags().GetStringSlice(flag); return v }
			relations := relationUseCases(st)
			edit, err := relations.Resolve(ctx, args[0], usecase.RelationsInput{
				AddDependencies: gids("add"), RemoveDependencies: gids("remove"),
				AddDependents: gids("add-dependent"), RemoveDependents: gids("remove-dependent"),
			})
			if err != nil {
				return "", "", flagErr(err, map[string]string{
					"add_dependencies": "add", "remove_dependencies": "remove",
					"add_dependents": "add-dependent", "remove_dependents": "remove-dependent",
				})
			}
			if len(edit.Dependencies) == 0 {
				return "", "", errors.New("--add / --remove / --add-dependent / --remove-dependent のどれかを指定してください")
			}
			return args[0], "依存関係を変更しました", relations.Apply(ctx, args[0], edit, nil)
		})
	depend.Flags().StringSlice("add", nil, "このタスクが待つタスク（gid）を追加")
	depend.Flags().StringSlice("remove", nil, "このタスクが待つタスク（gid）を外す")
	depend.Flags().StringSlice("add-dependent", nil, "このタスクを待つタスク（gid）を追加")
	depend.Flags().StringSlice("remove-dependent", nil, "このタスクを待つタスク（gid）を外す")

	kind := a.writeCommand("type <gid> <task|milestone|approval>", "タスクの種類を変える（タスク / マイルストーン / 承認）", cobra.ExactArgs(2),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			tasks := taskUseCases(st)
			u, err := tasks.ResolveUpdate(cmd.Context(), usecase.UpdateTaskInput{Subtype: &args[1]})
			if err != nil {
				return "", "", fieldErr(err)
			}
			return args[0], "種類を「" + task.SubtypeLabel(*u.Subtype) + "」にしました", tasks.UpdateHere(cmd.Context(), args[0], u)
		})

	approve := a.writeCommand("approve <gid> [approved|rejected|changes_requested|pending]", "承認タスクの承認の状態を変える（省略時は承認）", cobra.RangeArgs(1, 2),
		func(cmd *cobra.Command, st Store, args []string) (string, string, error) {
			v := "approved"
			tasks := taskUseCases(st)
			u := usecase.TaskUpdate{Approval: &v}
			if len(args) == 2 {
				var err error
				if u, err = tasks.ResolveUpdate(cmd.Context(), usecase.UpdateTaskInput{Approval: &args[1]}); err != nil {
					return "", "", fieldErr(err)
				}
			}
			d, err := query(st).GetTask(cmd.Context(), args[0])
			if err != nil {
				return "", "", err
			}
			if d.ResourceSubtype != "approval" {
				return "", "", fmt.Errorf("「%s」は承認タスクではありません（task type %s approval で変えられます）", d.Name, args[0])
			}
			return args[0], "承認の状態を「" + task.ApprovalLabel(*u.Approval) + "」にしました", tasks.UpdateHere(cmd.Context(), args[0], u)
		})
	return []*cobra.Command{like, follow, project, duplicate, attach, field, depend, kind, approve}
}

func (a *app) taskAssignCmd() *cobra.Command {
	return a.addDryRunFlag(&cobra.Command{
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
			tasks := taskUseCases(st)
			if _, err := tasks.Update(ctx, args[0], usecase.UpdateTaskInput{Assignee: &args[1]}); err != nil {
				return fieldErr(err)
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, "担当者を変更しました")
		},
	})
}

func (a *app) taskMoveCmd() *cobra.Command {
	var project, section string
	var mine bool
	cmd := &cobra.Command{
		Use:   "move <gid> --section <セクション> [--my-tasks]",
		Short: "タスクを別のセクションに移動する（--my-tasks でマイタスクのセクション）",
		Example: `  asagao task move 1203455468597469 --section Done
  asagao task move 1203455468597469 --section 今日やる --my-tasks`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			sections := &usecase.Sections{Store: st, Clock: time.Now}
			if mine {
				if project != "" {
					return fmt.Errorf("--my-tasks と --project は一緒に使えません")
				}
				if err := sections.MoveMine(ctx, args[0], section); err != nil {
					return err
				}
				return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, "移動しました: → "+section+"（マイタスク）")
			}
			if err := sections.Move(ctx, args[0], project, section); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0]}, "移動しました: → "+section)
		},
	}
	cmd.Flags().StringVar(&section, "section", "", "移動先のセクション（gid または名前）")
	cmd.Flags().StringVar(&project, "project", "", "プロジェクト（タスクが複数のプロジェクトに入っている場合）")
	cmd.Flags().BoolVar(&mine, "my-tasks", false, "マイタスクのセクション（今日やる・あとで など）に移す")
	_ = cmd.MarkFlagRequired("section")
	return a.addDryRunFlag(cmd)
}

func (a *app) sectionCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "section", Short: "セクション（リスト）"}
	var project string
	rename := &cobra.Command{
		Use:     "rename <セクション> <新しい名前> --project <プロジェクト>",
		Short:   "セクションの名前を変更する",
		Example: `  asagao section rename "In Pregoress" "In Progress" --project 新居`,
		Args:    cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			name := strings.Join(args[1:], " ")
			sections := &usecase.Sections{Store: st, Clock: time.Now}
			if err := sections.Rename(ctx, project, args[0], name); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "セクション名を変更しました: "+name)
		},
	}
	rename.Flags().StringVar(&project, "project", "", "プロジェクト（gid または名前）")
	_ = rename.MarkFlagRequired("project")
	cmd.AddCommand(a.addDryRunFlag(rename), a.sectionCreateCmd(), a.sectionRmCmd(), a.sectionMoveCmd(), a.sectionMineCmd())
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
			tags, err := query(st).ListTags(cmd.Context())
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(tags)
			}
			rows := [][]string{}
			for _, t := range tags {
				rows = append(rows, []string{"#" + t.Name, fmt.Sprintf("未完了 %d / 全 %d", t.Open, t.Total), t.Color})
			}
			printTable(cmd.OutOrStdout(), rows)
			return nil
		},
	})
	cmd.AddCommand(a.tagRenameCmd(), a.tagColorCmd(), a.tagRmCmd())
	return cmd
}

func (a *app) commentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "comment", Short: "コメント"}
	cmd.AddCommand(a.addDryRunFlag(&cobra.Command{
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
			comments := usecase.Comments{Store: st, Text: richtext.Converter{}, Clock: time.Now}
			gid, err := comments.Add(ctx, args[0], strings.Join(args[1:], " "))
			if err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: args[0], CommentID: gid}, "コメントしました")
		},
	}))
	return cmd
}

func (a *app) outboxCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "outbox", Short: "未送信の変更（オフライン中の編集・競合）を確認・解決する"}

	withOp := func(use, short string, action func() (usecase.ResolveAction, error)) *cobra.Command {
		return a.addDryRunFlag(&cobra.Command{
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
				if d, ok := st.(*dryRunStore); ok {
					return a.dryRunResolve(cmd, d, id, act)
				}
				msg, err := a.resolve(ctx, st, cmd.OutOrStdout(), id, act)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), msg)
				return nil
			},
		})
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
			ops, err := query(st).ListOps(cmd.Context())
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
				fmt.Fprintf(cmd.OutOrStdout(), "#%d  [%s]  %s\n", op.ID, readmodel.OpLabel(op), readmodel.DescribeOp(op))
				for _, c := range readmodel.ConflictLines(op) {
					fmt.Fprintf(cmd.OutOrStdout(), "      %s\n", c)
				}
				if heading, text := readmodel.OpNote(op); text != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "      %s: %s\n", heading, text)
				}
			}
			return nil
		},
	})

	var keep string
	resolve := withOp("resolve", "競合を解決する（--keep local: こちらの値で上書き / remote: Asana の値を採用）",
		func() (usecase.ResolveAction, error) {
			switch keep {
			case "local":
				return usecase.KeepLocal, nil
			case "remote":
				return usecase.KeepRemote, nil
			}
			return "", fmt.Errorf(`--keep は "local" か "remote" を指定してください`)
		})
	resolve.Flags().StringVar(&keep, "keep", "", "local または remote")
	_ = resolve.MarkFlagRequired("keep")

	var limit int
	discarded := &cobra.Command{
		Use:   "discarded",
		Short: "破棄した変更（Asana で削除されていたもの・初期化で消したもの）を、書いた内容の全文つきで表示する（新しい順）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.openStore(cmd.Context())
			if err != nil {
				return err
			}
			defer st.Close()
			ds, err := query(st).RecentDiscards(cmd.Context(), limit)
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

	fixed := func(act usecase.ResolveAction) func() (usecase.ResolveAction, error) {
		return func() (usecase.ResolveAction, error) { return act, nil }
	}
	cmd.AddCommand(resolve,
		withOp("retry", "送信に失敗した変更をもう一度送る", fixed(usecase.Retry)),
		withOp("discard", "未送信の変更を取り消す", fixed(usecase.Discard)),
	)
	return cmd
}

// resolve resolves an outbox entry (usecase.Outbox.Resolve), then sends it right away or refreshes
// the task from Asana as needed. Being offline is not an error: the next sync does it.
func (a *app) resolve(ctx context.Context, st Store, out io.Writer, id int64, act usecase.ResolveAction) (string, error) {
	r, err := (&usecase.Outbox{Store: st}).Resolve(ctx, id, act)
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
		case errors.Is(err, sync.ErrAccountChanged):
			return fmt.Sprintf("#%d: まだ送っていません。%v", id, accountChanged(ctx, st)), nil
		case errors.Is(err, sync.ErrPushBusy):
			return fmt.Sprintf("#%d: 別の asagao が送信中です。そちらで送られます", id), nil
		case err != nil:
			return fmt.Sprintf("#%d: 次の sync で送ります（オフライン: %v）", id, err), nil
		case push.Remaining > 0 || push.Failed > 0 || push.Conflicts > 0:
			return fmt.Sprintf("#%d: 送信できなかった変更があります。`asagao outbox list` で確認してください", id), nil
		case act == usecase.KeepLocal:
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
