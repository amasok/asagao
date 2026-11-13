package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

// writeResult is the JSON output of write commands.
type writeResult struct {
	TaskGID   string           `json:"task_gid,omitempty"`
	CommentID string           `json:"comment_gid,omitempty"`
	Sent      bool             `json:"sent"`
	Push      *sync.PushResult `json:"push,omitempty"`
	Error     string           `json:"error,omitempty"`
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
	// The gid may have changed from local:... to the real one.
	if r.TaskGID != "" && store.IsLocalGID(r.TaskGID) && r.Sent {
		r.TaskGID = ""
	}
	if a.json {
		return a.printJSON(r)
	}
	out := cmd.OutOrStdout()
	switch {
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

func (a *app) taskCreateCmd() *cobra.Command {
	var project, section, due, assignee, notes string
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
			nt := store.NewTask{Name: strings.Join(args, " "), Notes: notes}
			if nt.DueOn, err = parseDate(due, time.Now()); err != nil {
				return fmt.Errorf("--due: %w", err)
			}
			if project != "" {
				if nt.ProjectGID, err = st.ResolveProject(ctx, project); err != nil {
					return err
				}
				if section != "" {
					nt.SectionGID, err = st.ResolveSection(ctx, nt.ProjectGID, section)
				} else {
					nt.SectionGID, err = st.FirstSection(ctx, nt.ProjectGID)
				}
				if err != nil {
					return err
				}
			} else if nt.WorkspaceGID, err = st.DefaultWorkspace(ctx); err != nil {
				return err
			}
			if project == "" && assignee == "" {
				assignee = "me"
			}
			switch assignee {
			case "":
			case "me":
				if nt.AssigneeGID, err = st.MeGID(ctx); err != nil {
					return err
				}
			default:
				nt.AssigneeGID = assignee
			}
			gid, err := st.CreateTaskLocal(ctx, nt, time.Now())
			if err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{TaskGID: gid}, "タスクを作成しました: "+nt.Name)
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "プロジェクト（gid または名前）")
	cmd.Flags().StringVar(&section, "section", "", "セクション（gid または名前。省略時は先頭のセクション）")
	cmd.Flags().StringVar(&due, "due", "", "期日（YYYY-MM-DD / today / tomorrow / +Nd）")
	cmd.Flags().StringVar(&assignee, "assignee", "", `担当者（"me" またはユーザーの gid。プロジェクト未指定なら me）`)
	cmd.Flags().StringVar(&notes, "notes", "", "説明")
	return cmd
}

func (a *app) commentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "comment", Short: "コメント"}
	cmd.AddCommand(&cobra.Command{
		Use:   "add <タスクの gid> <コメント>",
		Short: "タスクにコメントする",
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
			gid, err := st.AddCommentLocal(ctx, args[0], strings.Join(args[1:], " "), me, time.Now())
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

	withOp := func(use, short string, fn func(ctx context.Context, st *store.Store, op *store.Op) (string, error)) *cobra.Command {
		return &cobra.Command{
			Use:   use + " <id>",
			Short: short,
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := strconv.ParseInt(args[0], 10, 64)
				if err != nil {
					return fmt.Errorf("id は数値で指定してください: %q", args[0])
				}
				ctx := cmd.Context()
				st, err := a.openStore(ctx)
				if err != nil {
					return err
				}
				defer st.Close()
				op, err := st.GetOp(ctx, id)
				if err != nil {
					return err
				}
				msg, err := fn(ctx, st, op)
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
			if len(ops) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "未送信の変更はありません")
				return nil
			}
			for _, op := range ops {
				fmt.Fprintf(cmd.OutOrStdout(), "#%d  [%s]  %s\n", op.ID, stateLabel(op.State), DescribeOp(op))
				for k, v := range op.Conflict {
					fmt.Fprintf(cmd.OutOrStdout(), "      %s: こちら=%v / Asana=%v\n", k, op.Payload[k], v)
				}
				if op.LastError != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "      エラー: %s\n", op.LastError)
				}
			}
			return nil
		},
	})

	var keep string
	resolve := withOp("resolve", "競合を解決する（--keep local: こちらの値で上書き / remote: Asana の値を採用）",
		func(ctx context.Context, st *store.Store, op *store.Op) (string, error) {
			if op.State != store.OpConflict {
				return "", fmt.Errorf("#%d は競合していません（状態: %s）", op.ID, stateLabel(op.State))
			}
			switch keep {
			case "local":
				if err := st.ResolveKeepLocal(ctx, op.ID); err != nil {
					return "", err
				}
				if _, err := a.pushNow(ctx, st); err != nil {
					return fmt.Sprintf("#%d: こちらの値を送ります（オフラインのため次の sync で送信）", op.ID), nil
				}
				return fmt.Sprintf("#%d: こちらの値で上書きしました", op.ID), nil
			case "remote":
				return a.discard(ctx, st, op)
			}
			return "", fmt.Errorf(`--keep は "local" か "remote" を指定してください`)
		})
	resolve.Flags().StringVar(&keep, "keep", "", "local または remote")
	_ = resolve.MarkFlagRequired("keep")

	cmd.AddCommand(resolve,
		withOp("retry", "送信に失敗した変更をもう一度送る", func(ctx context.Context, st *store.Store, op *store.Op) (string, error) {
			if err := st.RetryOp(ctx, op.ID); err != nil {
				return "", err
			}
			if _, err := a.pushNow(ctx, st); err != nil {
				return fmt.Sprintf("#%d: 次の sync で送ります（%v）", op.ID, err), nil
			}
			return fmt.Sprintf("#%d: 送信しました（失敗した場合は outbox list に残ります）", op.ID), nil
		}),
		withOp("discard", "未送信の変更を取り消す", a.discard),
	)
	return cmd
}

// discard drops a local change and refreshes the task from Asana when online.
func (a *app) discard(ctx context.Context, st *store.Store, op *store.Op) (string, error) {
	if err := st.DiscardOp(ctx, op.ID); err != nil {
		return "", err
	}
	if op.Kind != store.OpCreateTask {
		if err := a.fetchDetails(ctx, st, op.TaskGID); err != nil {
			return fmt.Sprintf("#%d を取り消しました（Asana の最新値は次の sync で反映されます）", op.ID), nil
		}
	}
	return fmt.Sprintf("#%d を取り消しました", op.ID), nil
}

func stateLabel(s string) string {
	switch s {
	case store.OpPending:
		return "未送信"
	case store.OpConflict:
		return "競合"
	case store.OpFailed:
		return "失敗"
	}
	return s
}

// DescribeOp is a one-line, human-readable description of an outbox entry.
func DescribeOp(op store.Op) string {
	name := op.TaskName
	if name == "" {
		name = op.TaskGID
	}
	switch op.Kind {
	case store.OpCreateTask:
		return "作成: " + name
	case store.OpAddComment:
		text, _ := op.Payload["text"].(string)
		if r := []rune(text); len(r) > 30 {
			text = string(r[:30]) + "…"
		}
		return fmt.Sprintf("コメント: %s「%s」", name, text)
	}
	var parts []string
	for _, k := range []string{"completed", "name", "notes", "due_on", "assignee"} {
		v, ok := op.Payload[k]
		if !ok {
			continue
		}
		switch k {
		case "completed":
			if v == true {
				parts = append(parts, "完了にする")
			} else {
				parts = append(parts, "未完了に戻す")
			}
		case "name":
			parts = append(parts, fmt.Sprintf("名前→%v", v))
		case "notes":
			parts = append(parts, "説明を変更")
		case "due_on":
			if v == "" {
				parts = append(parts, "期日を削除")
			} else {
				parts = append(parts, fmt.Sprintf("期日→%v", v))
			}
		case "assignee":
			parts = append(parts, "担当者を変更")
		}
	}
	return name + ": " + strings.Join(parts, "、")
}
