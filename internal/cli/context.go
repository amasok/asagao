package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app" // the cli package has its own type app
	"github.com/amasok/asagao/internal/repoctx"
)

// getwd is where .asana.json is looked for from (the tests point it elsewhere, so that a
// .asana.json around the source tree does not change what they do).
var getwd = os.Getwd

// repoContext is the .asana.json for the current folder (nil when there is none, or with
// --no-context).
func (a *app) repoContext() (*repoctx.Context, error) {
	if a.noContext {
		return nil, nil
	}
	dir, err := getwd()
	if err != nil {
		return nil, err
	}
	return repoctx.Find(dir)
}

// noteContext says on the error output that a default came from .asana.json (the output
// itself stays as it is, for --json).
func noteContext(cmd *cobra.Command, c *repoctx.Context, what string) {
	fmt.Fprintf(cmd.ErrOrStderr(), "（%s の%s）\n", c.Path, what)
}

func (a *app) contextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "このフォルダの .asana.json（作業中のプロジェクト・セクション・タスク）を表示する",
		Long: "カレントディレクトリから上に向かって .asana.json を探し（リポジトリのルートかホームフォルダまで）、\n" +
			"書かれたプロジェクト・セクション・タスクをキャッシュで引いて表示します。\n\n" +
			".asana.json の例:\n" +
			`  {"project": "新居", "section": "家具", "task": "1203455468597469"}` + "\n\n" +
			"project と section は gid か名前、task は gid。どれも省略できます。\n" +
			"task create と task list は、--project がないとき .asana.json のプロジェクトを使います\n" +
			"（task create は section も）。task show は gid がないとき .asana.json のタスクを表示します。\n" +
			"--no-context で .asana.json を使わずに動きます。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := a.repoContext()
			if err != nil {
				return err
			}
			if c == nil {
				if a.json {
					return a.printJSON(struct {
						Found bool `json:"found"`
					}{})
				}
				fmt.Fprintln(cmd.OutOrStdout(), ".asana.json はありません（このフォルダから、リポジトリのルートかホームフォルダまで）")
				return nil
			}
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			w := usecase.LookupWorkContext(ctx, query(st), c)
			if a.json {
				return a.printJSON(struct {
					Found bool `json:"found"`
					usecase.WorkContext
				}{true, w})
			}
			printWorkContext(cmd.OutOrStdout(), w)
			return nil
		},
	}
}

func printWorkContext(out io.Writer, w usecase.WorkContext) {
	fmt.Fprintln(out, ".asana.json: "+w.Path)
	ref := func(label string, r *usecase.ContextRef, extra string) {
		switch {
		case r == nil:
			return
		case r.Error != "":
			fmt.Fprintf(out, "%s: %s（%s）\n", label, r.Value, r.Error)
		case r.Name == "":
			fmt.Fprintf(out, "%s: %s\n", label, r.GID)
		default:
			fmt.Fprintf(out, "%s: %s (%s)%s\n", label, r.Name, r.GID, extra)
		}
	}
	ref("プロジェクト", w.Project, "")
	ref("セクション", w.Section, "")
	if w.Task != nil {
		extra := map[bool]string{true: "  [完了]", false: "  [未完了]"}[w.Task.Completed]
		if w.Task.DueOn != "" {
			extra += "  期日 " + w.Task.DueOn
		}
		ref("タスク", &w.Task.ContextRef, extra)
	}
}
