package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *app) projectCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "プロジェクトの一覧"}
	var all bool
	list := &cobra.Command{
		Use:   "list",
		Short: "プロジェクトとセクションを一覧表示する（デフォルトは参加しているプロジェクト）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			projects, err := st.ListProjects(ctx, !all)
			if err != nil {
				return err
			}
			if a.json {
				return a.printJSON(projects)
			}
			out := cmd.OutOrStdout()
			for _, p := range projects {
				fmt.Fprintf(out, "%s  (%s)\n", p.Name, p.GID)
				for _, s := range p.Sections {
					fmt.Fprintf(out, "  - %s  (%s)\n", s.Name, s.GID)
				}
			}
			return nil
		},
	}
	list.Flags().BoolVar(&all, "all", false, "参加していないプロジェクトも表示する")
	cmd.AddCommand(list)
	return cmd
}
