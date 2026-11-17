package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/repoctx"
	"github.com/amasok/asagao/internal/termsafe"
)

// The tools and prompts for an agent working in a repository: get_context reads the
// repository's .asana.json, and the prompts are the routine requests (a standup, sorting
// out overdue tasks, a weekly review) written once, so that they are asked the same way.

// ContextOutput is get_context's result.
type ContextOutput struct {
	// Found is false when there is no .asana.json (the rest is then empty).
	Found bool `json:"found"`
	app.WorkContext
}

func (s *Server) addAgentTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "get_context", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		Description: "Get the project, section and task this repository works on, from its .asana.json " +
			"(looked for from the server's working directory up to the repository root), with their gids and names from the cache. " +
			"Use the project as the default for list_tasks and create_task, and the task for progress comments."}, s.getContext)

	project := []*mcp.PromptArgument{{Name: "project",
		Description: "project gid or name (default: the project of .asana.json; without one, my tasks in every project)"}}
	srv.AddPrompt(&mcp.Prompt{Name: "standup", Title: "スタンドアップ",
		Description: "Yesterday / today / blockers from my Asana tasks, for a daily standup.", Arguments: project}, s.prompt(standupPrompt))
	srv.AddPrompt(&mcp.Prompt{Name: "triage_overdue", Title: "期限切れの整理",
		Description: "Go through my overdue tasks and propose what to do with each (new date, complete, hand over).", Arguments: project}, s.prompt(triagePrompt))
	srv.AddPrompt(&mcp.Prompt{Name: "weekly_review", Title: "週次の振り返り",
		Description: "What was done this week, what is late, and what comes next week; optionally a project status update.", Arguments: project}, s.prompt(weeklyPrompt))
}

// repoContext is the .asana.json for ContextDir (nil when there is none, or ContextDir is
// not set).
func (s *Server) repoContext() (*repoctx.Context, error) {
	if s.ContextDir == "" {
		return nil, nil
	}
	return repoctx.Find(s.ContextDir)
}

func (s *Server) getContext(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, ContextOutput, error) {
	c, err := s.repoContext()
	if err != nil || c == nil {
		return nil, ContextOutput{}, err
	}
	return nil, ContextOutput{Found: true, WorkContext: app.LookupWorkContext(ctx, s.query(), c)}, nil
}

// promptScope is what a prompt works on: a project (the argument, else .asana.json's) or
// all my tasks.
type promptScope struct {
	// Project is the project's gid ("" for my tasks in every project); Name its name.
	Project, Name string
	// From says where the project came from, for the text ("" for the argument).
	From string
	// Task is .asana.json's task ("" if none).
	Task, TaskName string
}

func (s *Server) scope(ctx context.Context, arg string) (promptScope, error) {
	var sc promptScope
	c, err := s.repoContext()
	if err != nil {
		return sc, err
	}
	if arg == "" && c != nil {
		arg, sc.From = c.Project, c.Path
	}
	if c != nil && c.Task != "" {
		sc.Task = c.Task
		if d, err := s.query().GetTask(ctx, c.Task); err == nil {
			sc.TaskName = d.Name
		}
	}
	if arg == "" {
		return sc, nil
	}
	gid, err := s.query().ResolveProject(ctx, arg)
	if err != nil {
		return sc, fmt.Errorf("project: %w", err)
	}
	sc.Project, sc.Name = gid, gid
	if ps, err := s.query().ListProjects(ctx, false); err == nil {
		for _, p := range ps {
			if p.GID == gid {
				sc.Name = p.Name
			}
		}
	}
	return sc, nil
}

// prompt makes the handler of a prompt whose text is build(scope, today, readOnly).
func (s *Server) prompt(build func(sc promptScope, today string, readOnly bool) string) mcp.PromptHandler {
	return func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		sc, err := s.scope(ctx, strings.TrimSpace(req.Params.Arguments["project"]))
		if err != nil {
			return nil, err
		}
		// Names come from Asana (through the cache): no terminal control characters.
		text := termsafe.String(build(sc, s.Now().Format("2006-01-02"), s.ReadOnly))
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: text}}}}, nil
	}
}

// target says what the prompt works on, and the arguments that select it in list_tasks.
func (sc promptScope) target() (what, args string) {
	if sc.Project == "" {
		return "自分のタスク（すべてのプロジェクト）", `assignee: "me"`
	}
	what = fmt.Sprintf("プロジェクト「%s」（gid %s）", sc.Name, sc.Project)
	if sc.From != "" {
		what += "。" + sc.From + " で指定されたもの"
	}
	return what, fmt.Sprintf(`project: "%s"`, sc.Project)
}

func (sc promptScope) taskLine() string {
	if sc.Task == "" {
		return ""
	}
	name := sc.TaskName
	if name == "" {
		name = "（キャッシュにありません）"
	}
	return fmt.Sprintf("\n.asana.json の作業中のタスク: 「%s」（gid %s）。これは必ず取り上げる。", name, sc.Task)
}

const promptCommon = `
- 最初に sync を呼んでキャッシュを新しくする（オフラインならキャッシュのままでよい。そのときはそう書く）。
- タスクは名前と gid で示す。読めない名前はそのまま書く。
- 答えは日本語で、短く。`

func writeRule(readOnly bool) string {
	if readOnly {
		return "\n- このサーバーは読み取り専用。変更は提案だけにする。"
	}
	return "\n- Asana を変える前に（update_task・add_comment など）、何をするかを一覧で示し、ユーザーの了承を得てから行う。"
}

func standupPrompt(sc promptScope, today string, readOnly bool) string {
	what, args := sc.target()
	return fmt.Sprintf(`今日は %s。%s について、スタンドアップの報告を作ってください。%s

1. 昨日やったこと: list_tasks で %s, completion: "completed", modified_within_days: 1 を見る（%s の前日以降に完了したもの）。
2. 今日やること: list_tasks で %s, due_before: "today" の未完了と、期限切れ（overdue: true）を見る。
3. 困っていること: 期限切れのタスクと、get_task で dependencies（待っているタスク）が未完了のものを挙げる。

それぞれ 3〜5 行の箇条書きにする。%s%s`, today, what, sc.taskLine(), args, today, args, promptCommon, writeRule(true))
}

func triagePrompt(sc promptScope, today string, readOnly bool) string {
	what, args := sc.target()
	return fmt.Sprintf(`今日は %s。%s の期限切れのタスクを整理してください。%s

1. list_tasks で %s, overdue: true を見る。
2. 1 件ずつ get_task で中身とコメントを見て、次のどれかを理由つきで提案する: 新しい期日（due_on）/ 完了にする / ほかの人に任せる / 不要なら完了にしてコメントで理由を残す。
3. 表にまとめる（タスク・期日・提案・理由）。%s%s`, today, what, sc.taskLine(), args, promptCommon, writeRule(readOnly))
}

func weeklyPrompt(sc promptScope, today string, readOnly bool) string {
	what, args := sc.target()
	status := ""
	if sc.Project != "" {
		status = "\n4. 求められたら、この内容でプロジェクトのステータス更新（create_status_update）の下書きを作る。投稿は了承を得てから。"
	}
	return fmt.Sprintf(`今日は %s。%s について、この 1 週間の振り返りをしてください。%s

1. 終わったこと: list_tasks で %s, completion: "completed", modified_within_days: 7。
2. 遅れていること: list_tasks で %s, overdue: true。
3. 来週やること: list_tasks で %s, due_after: "tomorrow", due_before: "+7d"。期日のない未完了タスクのうち、最近動いたもの（modified_within_days: 7）も挙げる。%s

見出しごとに箇条書きにし、最後に一言で全体の様子を書く。%s%s`, today, what, sc.taskLine(), args, args, args, status, promptCommon, writeRule(readOnly))
}
