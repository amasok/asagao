package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
	"github.com/amasok/asagao/internal/ui/format"
)

// Goals, portfolios and time periods (#7): read only. Online, they are fetched from Asana
// first; offline (or with --offline) the cache is shown.

func goalUseCases(st Store) *usecase.Goals { return &usecase.Goals{Store: st, Clock: time.Now} }

// staleNote tells that Asana could not be asked, so what is shown is the cache.
func staleNote(w io.Writer, err error) {
	if err != nil && !errors.Is(err, sync.ErrOffline) {
		fmt.Fprintf(w, "（Asana から取れなかったため、キャッシュの内容です: %v）\n", err)
	}
}

func (a *app) goalCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "goal", Short: "ゴールの一覧・詳細（読むだけ。Advanced プラン以上）"}
	var offline bool
	var period string
	list := &cobra.Command{
		Use:   "list [--period current|期間]",
		Short: "ゴールを一覧表示する（--period で期間に絞る。current は今日を含む期間）",
		Example: `  asagao goal list
  asagao goal list --period current
  asagao goal list --period "FY26"   # その年の四半期のゴールも含む`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			var stale error
			if !offline {
				stale = a.fetch(ctx, st, (*sync.Syncer).FetchGoals)
			}
			l, err := goalUseCases(st).List(ctx, period)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(l)
			}
			printGoalList(cmd.OutOrStdout(), l, period)
			staleNote(cmd.ErrOrStderr(), stale)
			return nil
		},
	}
	list.Flags().StringVar(&period, "period", "", "期間の名前（例: Q1 FY26）か gid、または current")
	show := &cobra.Command{
		Use:   "show <ゴール>",
		Short: "ゴールの詳細（説明・進み具合・期間・支えているサブゴールやプロジェクト）を表示する",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			goals := goalUseCases(st)
			var stale error
			if !offline {
				if s, err := a.onlineSyncer(ctx, st); err == nil {
					gid, err := goals.ResolveGoal(ctx, args[0])
					if errors.Is(err, readmodel.ErrNotFound) {
						// Not cached yet: fetch the list, or take a gid as it is.
						stale = s.FetchGoals(ctx)
						if gid, err = goals.ResolveGoal(ctx, args[0]); errors.Is(err, readmodel.ErrNotFound) && isGID(args[0]) {
							gid, err = args[0], nil
						}
					}
					if err == nil {
						if ferr := s.FetchGoal(ctx, gid); ferr != nil {
							stale = ferr
						}
					}
				}
			}
			g, err := goals.Get(ctx, args[0])
			if err != nil {
				if stale != nil && errors.Is(err, readmodel.ErrNotFound) {
					return fmt.Errorf("%w（Asana から取れませんでした: %v）", err, stale)
				}
				return err
			}
			if a.json {
				return a.printJSON(g)
			}
			printGoal(cmd.OutOrStdout(), g, time.Now())
			staleNote(cmd.ErrOrStderr(), stale)
			return nil
		},
	}
	periods := &cobra.Command{
		Use:   "periods",
		Short: "ゴールの期間（年度・半期・四半期）を一覧表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			var stale error
			if !offline {
				stale = a.fetch(ctx, st, (*sync.Syncer).FetchGoals)
			}
			l, err := goalUseCases(st).List(ctx, "")
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(l.Periods)
			}
			out := cmd.OutOrStdout()
			if len(l.Periods) == 0 {
				fmt.Fprintln(out, "期間はありません")
			}
			today := time.Now().Format("2006-01-02")
			for _, p := range l.Periods {
				mark := "  "
				if p.Contains(today) {
					mark = "▶ "
				}
				fmt.Fprintf(out, "%s%s  %s 〜 %s  (%s)\n", mark, p.DisplayName, p.StartOn, p.EndOn, p.GID)
			}
			staleNote(cmd.ErrOrStderr(), stale)
			return nil
		},
	}
	cmd.PersistentFlags().BoolVar(&offline, "offline", false, "Asana に問い合わせず、ローカルストアだけで表示する")
	cmd.AddCommand(list, show, periods)
	return cmd
}

func (a *app) portfolioCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "portfolio", Short: "自分のポートフォリオの一覧・中身（読むだけ。Advanced プラン以上）"}
	var offline bool
	list := &cobra.Command{
		Use:   "list",
		Short: "自分がオーナーのポートフォリオを一覧表示する",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			var stale error
			if !offline {
				stale = a.fetch(ctx, st, (*sync.Syncer).FetchPortfolios)
			}
			goals := goalUseCases(st)
			ps, err := goals.Portfolios(ctx)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(ps)
			}
			out := cmd.OutOrStdout()
			if len(ps) == 0 {
				msg := "ポートフォリオはありません（Asana の API では、自分がオーナーのものだけが見えます）"
				if at, _ := goals.PortfoliosFetchedAt(ctx); at == "" {
					msg = "ポートフォリオはまだ取得していません（オンラインで実行してください）"
				}
				fmt.Fprintln(out, msg)
			}
			for _, p := range ps {
				fmt.Fprintf(out, "%s  (%s)\n", portfolioLine(p.Name, p.Status, p.Owner, p.DueOn), p.GID)
			}
			staleNote(cmd.ErrOrStderr(), stale)
			return nil
		},
	}
	show := &cobra.Command{
		Use:   "show <ポートフォリオ>",
		Short: "ポートフォリオの中身（プロジェクトとポートフォリオ、それぞれの最新のステータス）を表示する",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			goals := goalUseCases(st)
			var stale error
			if !offline {
				if s, err := a.onlineSyncer(ctx, st); err == nil {
					gid, err := goals.ResolvePortfolio(ctx, args[0])
					if errors.Is(err, readmodel.ErrNotFound) {
						stale = s.FetchPortfolios(ctx)
						if gid, err = goals.ResolvePortfolio(ctx, args[0]); errors.Is(err, readmodel.ErrNotFound) && isGID(args[0]) {
							gid, err = args[0], nil
						}
					}
					if err == nil {
						if ferr := s.FetchPortfolio(ctx, gid); ferr != nil {
							stale = ferr
						}
					}
				}
			}
			p, err := goals.Portfolio(ctx, args[0])
			if err != nil {
				if stale != nil && errors.Is(err, readmodel.ErrNotFound) {
					return fmt.Errorf("%w（Asana から取れませんでした: %v）", err, stale)
				}
				return err
			}
			if a.json {
				return a.printJSON(p)
			}
			printPortfolio(cmd.OutOrStdout(), p)
			staleNote(cmd.ErrOrStderr(), stale)
			return nil
		},
	}
	cmd.PersistentFlags().BoolVar(&offline, "offline", false, "Asana に問い合わせず、ローカルストアだけで表示する")
	cmd.AddCommand(list, show)
	return cmd
}

// fetch runs a refresh from Asana when online (nil when offline: the cache is shown).
func (a *app) fetch(ctx context.Context, st Store, f func(*sync.Syncer, context.Context) error) error {
	s, err := a.onlineSyncer(ctx, st)
	if err != nil {
		return nil
	}
	return f(s, ctx)
}

// isGID reports whether arg looks like an Asana gid (digits only).
func isGID(arg string) bool {
	return arg != "" && strings.Trim(arg, "0123456789") == ""
}

func goalLine(g readmodel.GoalView) string {
	parts := []string{"[" + format.GoalStatusLabel(g.Status) + "] " + g.Name}
	if t := format.GoalMetricText(g.Metric); t != "" {
		parts = append(parts, t)
	}
	if g.TimePeriod != nil {
		parts = append(parts, g.TimePeriod.DisplayName)
	} else if g.DueOn != "" {
		parts = append(parts, "期日 "+g.DueOn)
	}
	if g.Owner != nil {
		parts = append(parts, g.Owner.Name)
	}
	return strings.Join(parts, " · ")
}

func printGoalList(out io.Writer, l *readmodel.GoalList, period string) {
	switch {
	case l.Period != nil:
		fmt.Fprintf(out, "期間: %s（%s 〜 %s）\n", l.Period.DisplayName, l.Period.StartOn, l.Period.EndOn)
	case period == usecase.PeriodCurrent:
		fmt.Fprintln(out, "期間: 今日を含む期間")
	}
	if len(l.Goals) == 0 {
		if l.FetchedAt == "" {
			fmt.Fprintln(out, "ゴールはまだ取得していません（オンラインで実行してください）")
		} else {
			fmt.Fprintln(out, "ゴールはありません")
		}
	}
	for _, g := range l.Goals {
		fmt.Fprintf(out, "%s  (%s)\n", goalLine(g), g.GID)
	}
}

func printGoal(out io.Writer, g *readmodel.GoalDetail, now time.Time) {
	fmt.Fprintf(out, "%s  (%s)\n", g.Name, g.GID)
	line := func(label, v string) {
		if v != "" {
			fmt.Fprintf(out, "  %s: %s\n", label, v)
		}
	}
	line("ステータス", format.GoalStatusLabel(g.Status))
	line("進み具合", format.GoalMetricText(g.Metric))
	if g.TimePeriod != nil {
		line("期間", g.TimePeriod.DisplayName)
	}
	if g.StartOn != "" || g.DueOn != "" {
		line("日付", strings.TrimSpace(g.StartOn+" 〜 "+g.DueOn))
	}
	if g.Owner != nil {
		line("オーナー", g.Owner.Name)
	}
	if g.WorkspaceLevel {
		line("範囲", "ワークスペース全体")
	} else {
		line("チーム", g.TeamName)
	}
	if notes := strings.TrimSpace(g.Notes); notes != "" {
		fmt.Fprintf(out, "\n説明:\n%s\n", indent(notes))
	}
	if g.FetchedAt == "" {
		fmt.Fprintln(out, "\n（サブゴールや支えているプロジェクトは、オンラインで開いたときに取得します）")
		return
	}
	if len(g.Supporting) > 0 {
		fmt.Fprintln(out, "\n支えているもの:")
		for _, s := range g.Supporting {
			fmt.Fprintf(out, "  %s  %s  (%s)\n", supportLabel(s.Type), s.Name, s.GID)
		}
	}
}

// supportLabel names what supports a goal.
func supportLabel(t string) string {
	switch t {
	case "goal":
		return "サブゴール"
	case "project":
		return "プロジェクト"
	case "portfolio":
		return "ポートフォリオ"
	case "task":
		return "タスク"
	}
	return t
}

func portfolioLine(name string, st *readmodel.StatusBrief, owner *readmodel.UserRef, due string) string {
	parts := []string{name}
	if st != nil {
		parts[0] = "[" + format.StatusLabel(st.StatusType) + "] " + name
	}
	if due != "" {
		parts = append(parts, "期日 "+due)
	}
	if owner != nil {
		parts = append(parts, owner.Name)
	}
	return strings.Join(parts, " · ")
}

func printPortfolio(out io.Writer, p *readmodel.PortfolioDetail) {
	fmt.Fprintf(out, "%s  (%s)\n", portfolioLine(p.Name, p.Status, p.Owner, p.DueOn), p.GID)
	if p.PermalinkURL != "" {
		fmt.Fprintf(out, "  URL: %s\n", p.PermalinkURL)
	}
	if p.FetchedAt == "" {
		fmt.Fprintln(out, "\n（中身は、オンラインで開いたときに取得します）")
		return
	}
	if len(p.Items) == 0 {
		fmt.Fprintln(out, "\n（中身はありません）")
		return
	}
	fmt.Fprintln(out)
	for _, it := range p.Items {
		kind := "プロジェクト"
		if it.Type == "portfolio" {
			kind = "ポートフォリオ"
		}
		name := it.Name
		if it.Archived {
			name += "（アーカイブ済み）"
		}
		fmt.Fprintf(out, "  %s  %s  (%s)\n", kind, portfolioLine(name, it.Status, it.Owner, it.DueOn), it.GID)
	}
}
