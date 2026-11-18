package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/richtext"
)

// `project brief`: a project's brief (#60). Shown as editable Markdown (its tables and
// embeds as asana-html blocks, see richtext), so that it can be written to a file, edited
// and given back with --file; --edit opens it in the editor. Changes are queued like any
// other (offline too); a brief changed in Asana meanwhile is a conflict in the outbox.

// briefJSON is what `project brief --json` prints.
type briefJSON struct {
	ProjectGID string `json:"project_gid"`
	State      string `json:"state"` // none, available, unavailable, or "" (not fetched yet)
	Title      string `json:"title,omitempty"`
	Markdown   string `json:"markdown,omitempty"`
	Pending    bool   `json:"pending,omitempty"`
}

func (a *app) projectBriefCmd() *cobra.Command {
	var title, file string
	var edit, offline bool
	cmd := &cobra.Command{
		Use:   "brief <プロジェクト> [--title タイトル] [--file ファイル | --edit]",
		Short: "プロジェクトブリーフ（概要のページ）を表示する・書く（オフラインでも積む）",
		Long: `プロジェクトブリーフを表示します。--title・--file・--edit を付けると、書き換えます（無ければ作ります）。
本文はマークダウンです。表や埋め込み（外部メディア・マイルストーン・ゴール）は ` + "```asana-html" + ` のブロックに
Asana の HTML のまま入っています。ブロックを残せば、そのまま送り返します（表の文字はその中で直せます）。`,
		Example: `  asagao project brief 新居 > brief.md          # 今のブリーフをファイルに
  asagao project brief 新居 --file brief.md      # 直したファイルで書き換える
  asagao project brief 新居 --edit               # エディタで書き換える
  echo "## 目的" | asagao project brief 新居 --title 計画 --file -
  asagao project brief rm 新居                   # 削除する`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if file != "" && edit {
				return errors.New("--file と --edit は一緒に使えません")
			}
			writing := cmd.Flags().Changed("title") || file != "" || edit
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			gid, err := query(st).ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			if !offline && !a.dryRun {
				// the latest brief: what is shown, and what an edit starts from
				if s, err := a.onlineSyncer(ctx, st); err == nil {
					if err := s.FetchProjectOverview(ctx, gid); err != nil && !writing {
						fmt.Fprintf(cmd.ErrOrStderr(), "（Asana から取れなかったため、キャッシュの内容です: %v）\n", err)
					}
				}
			}
			briefs := &usecase.Briefs{Store: st, Text: richtext.Converter{}, Clock: time.Now}
			if !writing {
				return a.printBrief(cmd, st, gid)
			}
			var in usecase.BriefInput
			if cmd.Flags().Changed("title") {
				in.Title = &title
			}
			switch {
			case file != "":
				md, err := readBriefFile(cmd, file)
				if err != nil {
					return err
				}
				in.Markdown = &md
			case edit:
				if err := briefs.Available(ctx, gid); err != nil {
					return err
				}
				md, err := a.editBriefBody(ctx, st, gid)
				if err != nil {
					return err
				}
				in.Markdown = &md
			}
			changed, err := briefs.SaveHere(ctx, gid, in)
			if err != nil {
				return flagErr(err, map[string]string{})
			}
			if !changed {
				fmt.Fprintln(cmd.ErrOrStderr(), "変更はありません")
				return nil
			}
			return a.reportWrite(cmd, st, writeResult{}, "ブリーフを変更しました")
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "タイトル")
	cmd.Flags().StringVar(&file, "file", "", `本文のマークダウンのファイル（"-" で標準入力。全体を置き換える）`)
	cmd.Flags().BoolVar(&edit, "edit", false, "本文をエディタで書き換える（ui.editor・$VISUAL・$EDITOR）")
	cmd.Flags().BoolVar(&offline, "offline", false, "Asana に問い合わせず、ローカルストアだけを使う")
	cmd.AddCommand(a.projectBriefRmCmd())
	return a.addDryRunFlag(cmd)
}

func (a *app) projectBriefRmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <プロジェクト>",
		Short: "プロジェクトブリーフを削除する（オフラインでも積む）",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			st, err := a.openStore(ctx)
			if err != nil {
				return err
			}
			defer st.Close()
			briefs := &usecase.Briefs{Store: st, Text: richtext.Converter{}, Clock: time.Now}
			if _, err := briefs.Delete(ctx, args[0]); err != nil {
				return err
			}
			return a.reportWrite(cmd, st, writeResult{}, "ブリーフを削除しました")
		},
	}
	return a.addDryRunFlag(cmd)
}

// printBrief prints a project's brief as editable Markdown (a title line first, if it has
// one), or says why there is none.
func (a *app) printBrief(cmd *cobra.Command, st Store, gid string) error {
	o, err := query(st).ProjectOverview(cmd.Context(), gid)
	if err != nil {
		return err
	}
	out := briefJSON{ProjectGID: gid, State: o.BriefState, Pending: o.BriefPending}
	if o.Brief != nil {
		out.Title, out.Markdown = o.Brief.Title, briefMarkdown(o.Brief.HTMLText, o.Brief.Text)
	}
	if a.json {
		return a.printJSON(out)
	}
	w := cmd.OutOrStdout()
	switch {
	case o.Brief != nil:
		if out.Title != "" {
			fmt.Fprintf(cmd.ErrOrStderr(), "タイトル: %s\n", out.Title)
		}
		fmt.Fprintln(w, out.Markdown)
	case o.BriefState == "unavailable":
		return errors.New("このプロジェクトのブリーフは Asana から取得できません（プランか、API がプレビューのため）")
	case o.BriefState == "":
		return errors.New("ブリーフはまだ取得していません（オンラインで、もう一度実行してください）")
	default:
		fmt.Fprintln(cmd.ErrOrStderr(), "ブリーフはありません（--title・--file・--edit で作れます）")
	}
	if o.BriefPending {
		fmt.Fprintln(cmd.ErrOrStderr(), "（ブリーフの変更がまだ Asana に送られていません）")
	}
	return nil
}

// briefMarkdown is a brief's body as editable Markdown.
func briefMarkdown(htmlText, text string) string {
	if htmlText != "" {
		return richtext.BriefToEditableMarkdown(htmlText, nil)
	}
	return richtext.FromPlainEditable(text)
}

// readBriefFile reads the body from a file, or standard input for "-".
func readBriefFile(cmd *cobra.Command, path string) (string, error) {
	var r io.Reader = cmd.InOrStdin()
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return "", err
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, 4<<20))
	return string(b), err
}

// editBriefBody opens the brief's body in the editor and returns it as saved. The file is
// left (and named in the error) when the editor fails.
func (a *app) editBriefBody(ctx context.Context, st Store, gid string) (string, error) {
	o, err := query(st).ProjectOverview(ctx, gid)
	if err != nil {
		return "", err
	}
	body := ""
	if o.Brief != nil {
		body = briefMarkdown(o.Brief.HTMLText, o.Brief.Text)
	}
	f, err := os.CreateTemp("", "asagao-brief-*.md")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_, err = f.WriteString(body + "\n")
	f.Close()
	if err != nil {
		return "", err
	}
	args := strings.Fields(a.editorCommand())
	c := exec.CommandContext(ctx, args[0], append(args[1:], path)...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("エディタがエラーで終了しました（内容は %s に残っています）: %w", path, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	os.Remove(path)
	return strings.TrimSpace(string(b)), nil
}

// editorCommand is ui.editor, $VISUAL, $EDITOR, nvim or vi (in that order).
func (a *app) editorCommand() string {
	cmdline := a.cfg.UI.Editor
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if cmdline == "" {
			cmdline = os.Getenv(env)
		}
	}
	if cmdline == "" {
		cmdline = "vi"
		if _, err := exec.LookPath("nvim"); err == nil {
			cmdline = "nvim"
		}
	}
	return cmdline
}
