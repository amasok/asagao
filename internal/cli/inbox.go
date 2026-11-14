package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

// inboxOutput is the JSON output of `inbox`.
type inboxOutput struct {
	// Available is false when the notifications cannot be fetched (see Notice); Recent then
	// lists the tasks modified in the last 7 days instead.
	Available     bool                 `json:"available"`
	Notice        string               `json:"notice,omitempty"`
	Unread        int                  `json:"unread"`
	Notifications []store.Notification `json:"notifications"`
	Recent        []store.TaskSummary  `json:"recent,omitempty"`
}

// recentDays is how far back the recent updates (the inbox substitute) go.
const recentDays = 7

func (a *app) inboxCmd() *cobra.Command {
	var unread bool
	var limit int
	cmd := &cobra.Command{
		Use:   "inbox",
		Short: "受信トレイ（Asana の通知）を新しい順に表示する（ローカルストアから）",
		Long: "同期で取得した Asana の通知を、新しい順に表示します。● が未読です。\n" +
			"通知を取得できないとき（ログインに通知の権限がないなど）は、理由と、直近 7 日に更新されたタスクを表示します。",
		Example: `  asana-tui inbox
  asana-tui inbox --unread --json
  asana-tui inbox read 1234          # ID（末尾の数桁でも可）の通知を既読に
  asana-tui inbox read --all`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			out, err := loadInbox(ctx, st, unread, limit, a.cfg.Auth.Scopes, time.Now())
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(out)
			}
			w := cmd.OutOrStdout()
			if !out.Available {
				fmt.Fprintln(cmd.ErrOrStderr(), out.Notice)
				fmt.Fprintf(w, "最近の更新（直近 %d 日）:\n", recentDays)
				printTaskTable(w, out.Recent)
				return nil
			}
			if out.Notice != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), out.Notice)
			}
			printNotifications(w, out.Notifications, time.Now())
			fmt.Fprintf(w, "\n%d 件（未読 %d 件）\n", len(out.Notifications), out.Unread)
			return nil
		},
	}
	cmd.Flags().BoolVar(&unread, "unread", false, "未読の通知だけ")
	cmd.Flags().IntVar(&limit, "limit", 0, "最大件数（0 = 無制限）")
	cmd.AddCommand(a.inboxMarkCmd("read", true), a.inboxMarkCmd("unread", false))
	return cmd
}

func loadInbox(ctx context.Context, st *store.Store, unread bool, limit int, scopes []string, now time.Time) (inboxOutput, error) {
	state, err := st.InboxState(ctx)
	if err != nil {
		return inboxOutput{}, err
	}
	out := inboxOutput{Available: state.Available(), Notifications: []store.Notification{}}
	if out.Available && state.WriteForbidden {
		out.Notice = sync.InboxReadOnlyMessage(scopes)
	}
	if !out.Available {
		out.Notice = sync.InboxUnavailableMessage(state, scopes)
		out.Recent, err = st.ListTasks(ctx, store.TaskFilter{
			ModifiedAfter:  now.AddDate(0, 0, -recentDays).UTC().Format(time.RFC3339),
			SortByModified: true, Limit: limit,
		})
		return out, err
	}
	if out.Notifications, err = st.ListNotifications(ctx, store.NotificationFilter{UnreadOnly: unread, Limit: limit}); err != nil {
		return out, err
	}
	out.Unread, err = st.UnreadNotifications(ctx)
	return out, err
}

func (a *app) inboxMarkCmd(use string, read bool) *cobra.Command {
	var all bool
	short := "通知を既読にする（ID は `asana-tui inbox` の右端。末尾の数桁でも可）"
	if !read {
		short = "通知を未読に戻す"
	}
	cmd := &cobra.Command{
		Use:   use + " [<ID>...]",
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && !all {
				return errors.New("通知の ID を指定してください（すべて既読にするなら --all）")
			}
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			gids, err := resolveNotifications(ctx, st, args, all, read)
			if err != nil {
				return err
			}
			n, err := st.MarkNotificationsLocal(ctx, gids, read, time.Now())
			if errors.Is(err, store.ErrInboxReadOnly) {
				return errors.New(sync.InboxReadOnlyMessage(a.cfg.Auth.Scopes))
			}
			if err != nil {
				return err
			}
			state := "既読"
			if !read {
				state = "未読"
			}
			if n == 0 {
				if a.json {
					return a.printJSON(writeResult{Sent: true})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "すでに%sです\n", state)
				return nil
			}
			return a.reportWrite(cmd, st, writeResult{}, fmt.Sprintf("%d 件を%sにしました", n, state))
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, map[bool]string{true: "すべての通知を既読にする", false: "すべての通知を未読に戻す"}[read])
	return cmd
}

// resolveNotifications turns the arguments (gids or unique suffixes) into gids; all
// takes every cached notification not in the read state yet.
func resolveNotifications(ctx context.Context, st *store.Store, args []string, all, read bool) ([]string, error) {
	if all {
		ns, err := st.ListNotifications(ctx, store.NotificationFilter{UnreadOnly: read})
		if err != nil {
			return nil, err
		}
		var gids []string
		for _, n := range ns {
			if n.Read != read {
				gids = append(gids, n.GID)
			}
		}
		return gids, nil
	}
	gids := make([]string, 0, len(args))
	for _, a := range args {
		gid, err := st.ResolveNotification(ctx, a)
		if err != nil {
			return nil, err
		}
		gids = append(gids, gid)
	}
	return gids, nil
}

// printNotifications prints one line per notification: unread mark, when, who, what,
// the task, the story text, and the ID.
func printNotifications(w io.Writer, ns []store.Notification, now time.Time) {
	if len(ns) == 0 {
		fmt.Fprintln(w, "通知はありません")
		return
	}
	rows := make([][]string, 0, len(ns))
	for _, n := range ns {
		mark := " "
		if !n.Read {
			mark = "●"
		}
		if n.Pending {
			mark += "↑"
		}
		rows = append(rows, []string{mark, notificationTime(n.CreatedAt, now), n.ActorName,
			store.NotificationLabel(n.Subtype), n.TargetName, clipText(n.Text, 50), n.GID})
	}
	printTable(w, rows)
}

// notificationTime is a short local time: 15:04 today, 1/2 15:04 this year, else 2006-01-02.
func notificationTime(s string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return s
	}
	t = t.In(now.Location())
	switch {
	case t.Format(time.DateOnly) == now.Format(time.DateOnly):
		return t.Format("15:04")
	case t.Year() == now.Year():
		return t.Format("1/2 15:04")
	}
	return t.Format(time.DateOnly)
}

func clipText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
