package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

// agentGuide is the block to paste into CLAUDE.md (or AGENTS.md) so that an AI agent uses
// asagao for the repository's Asana tasks. README.md has the same text (a test checks it).
const agentGuide = `## Asana（asagao）

このリポジトリの作業は Asana で管理している。Asana の操作には ` + "`asagao`" + ` を使う。

- 作業中のプロジェクト・タスクは ` + "`.asana.json`" + ` にある。最初に ` + "`asagao context --json`" + ` で確かめる。
- 読む: ` + "`asagao task list --json`" + `（.asana.json のプロジェクトの未完了タスク）、` + "`asagao task show [<gid>] --json`" + `（gid を省くと .asana.json のタスク）、` + "`asagao task search <語> --json`" + `。
- 書く前に、同じコマンドに ` + "`--dry-run`" + ` を付けて、何が変わるかを確かめる。` + "`--dry-run`" + ` は Asana にも outbox にも何も書かない。
- 書く: ` + "`asagao comment add <gid> <本文>`" + `（進捗の報告）、` + "`asagao task complete <gid>`" + `、` + "`asagao task create <名前> [--due <日付>]`" + `（.asana.json のプロジェクトに作る）、` + "`asagao task move <gid> --section <セクション>`" + `。
- 書き込みはオフラインでも受け付け、次の同期で送る。` + "`--json`" + ` の ` + "`sent`" + ` が false なら未送信。勝手に再送したり ` + "`asagao outbox`" + ` を操作したりしない。
- プロジェクトのアーカイブ、ほかの人への割り当て、ほかの人が担当するタスクの完了は、ユーザーに頼まれたときだけ行う。
- MCP サーバー（` + "`asagao mcp`" + `）が使えるなら、` + "`get_context`" + ` で .asana.json を読み、定型の手順はプロンプト（standup・triage_overdue・weekly_review）を使う。
`

func (a *app) agentGuideCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "agent-guide",
		Short:   "AI エージェント向けの使い方（CLAUDE.md や AGENTS.md に貼るブロック）を表示する",
		Example: `  asagao agent-guide >> CLAUDE.md`,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprint(cmd.OutOrStdout(), agentGuide)
			return err
		},
	}
}
