package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// searches are the saved searches in searches.toml (wire: config.SearchFile).
func (a *app) searches() usecase.Searches { return usecase.Searches{File: a.wiring.Searches} }

func (a *app) searchesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "searches",
		Short: "保存した検索（searches.toml）の保存・一覧・削除・実行",
		Long: "検索の条件に名前を付けて保存し、あとで呼び出します。保存先は $XDG_CONFIG_HOME/asagao/searches.toml です。\n" +
			"期日の相対指定（today、+7d など）はそのまま保存し、実行した日から数えます。\n" +
			"検索語（--query）のある検索は `asagao search` と同じく全員の・完了済みも含むタスクを、\n" +
			"ない検索は `asagao task list` と同じく自分の未完了のタスク（--project なら全員の）を探します。",
	}
	cmd.AddCommand(a.searchesSaveCmd(), a.searchesListCmd(), a.searchesRmCmd(), a.searchesRunCmd())
	return cmd
}

func (a *app) searchesSaveCmd() *cobra.Command {
	var (
		f           listFlags
		q           string
		open, force bool
	)
	cmd := &cobra.Command{
		Use:   "save <名前>",
		Short: "検索の条件に名前を付けて保存する",
		Example: `  asagao searches save 今週 --due-before +7d
  asagao searches save 新居の未完了 --project 新居 --open
  asagao searches save 請求書 --query 請求書 --assignee me --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := f.savedSearch(cmd, args[0], q, open)
			if err != nil {
				return err
			}
			err = a.searches().Save(s, force)
			if errors.Is(err, usecase.ErrSearchExists) {
				return fmt.Errorf("%w（上書きするには --force）", err)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "保存しました: %s\n", strings.TrimSpace(args[0]))
			return nil
		},
	}
	f.register(cmd, "")
	cmd.Flags().StringVar(&q, "query", "", "検索語（タスク名・説明・コメントの全文検索）")
	cmd.Flags().BoolVar(&open, "open", false, "未完了のタスクだけ")
	cmd.Flags().BoolVar(&force, "force", false, "同じ名前の検索があれば上書きする")
	return cmd
}

// savedSearch is the search the flags of `searches save` describe: only what was given.
func (f *listFlags) savedSearch(cmd *cobra.Command, name, q string, open bool) (readmodel.SavedSearch, error) {
	s := readmodel.SavedSearch{
		Name: name, Query: q, Project: f.project, Section: f.section, Tag: f.tag, Field: f.field,
		DueBefore: f.dueBefore, DueAfter: f.dueAfter, Overdue: f.overdue,
		ModifiedWithinDays: f.modifiedWithin, Limit: f.limit,
	}
	if cmd.Flags().Changed("assignee") {
		s.Assignee = f.assignee
		if s.Assignee == "" {
			s.Assignee = "any"
		}
	}
	n := 0
	for _, c := range []struct {
		on    bool
		value string
	}{{open, "open"}, {f.completed, "completed"}, {f.all, "all"}} {
		if c.on {
			s.Completion = c.value
			n++
		}
	}
	if n > 1 {
		return s, errors.New("--open・--completed・--all はどれか 1 つだけ指定してください")
	}
	return s, nil
}

func (a *app) searchesListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "保存した検索を一覧表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ss, err := a.searches().List()
			if err != nil {
				return err
			}
			if a.json {
				if ss == nil {
					ss = []readmodel.SavedSearch{}
				}
				return a.printJSON(ss)
			}
			printSearches(cmd.OutOrStdout(), ss)
			return nil
		},
	}
}

func printSearches(w io.Writer, ss []readmodel.SavedSearch) {
	if len(ss) == 0 {
		fmt.Fprintln(w, "保存した検索はありません（asagao searches save <名前> で保存）")
		return
	}
	for _, s := range ss {
		cond := strings.Join(s.Conditions(), " ")
		if cond == "" {
			cond = "（条件なし: 自分の未完了のタスク）"
		}
		fmt.Fprintf(w, "%s\t%s\n", s.Name, cond)
	}
}

func (a *app) searchesRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <名前>",
		Aliases: []string{"remove", "delete"},
		Short:   "保存した検索を削除する",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.searches().Remove(args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "削除しました: %s\n", args[0])
			return nil
		},
	}
}

func (a *app) searchesRunCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "run <名前>",
		Short:   "保存した検索を実行する（ローカルストアから）",
		Example: "  asagao searches run 今週\n  asagao searches run 今週 --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.searches().Get(args[0])
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("limit") {
				if limit < 0 {
					return errors.New("--limit: 0 以上を指定してください")
				}
				s.Limit = limit
			}
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			tf, err := st.App.Query().SavedSearchFilter(ctx, s.Name, s.Spec(), time.Now())
			if err != nil {
				return err
			}
			tasks, err := st.App.Query().ListTasks(ctx, tf)
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
	cmd.Flags().IntVar(&limit, "limit", 0, "最大件数（保存した件数より優先。0 = 無制限）")
	return cmd
}
