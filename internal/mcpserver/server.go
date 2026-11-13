// Package mcpserver exposes asana-tui's local store and outbox as an MCP server.
//
// Reads come from the local store (fast, works offline). Writes go through the same
// outbox as the TUI and CLI: they are applied locally at once and sent to Asana right
// away when online, or by the next sync otherwise.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	stdsync "sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asana-tui/internal/dates"
	"github.com/amasok/asana-tui/internal/store"
	"github.com/amasok/asana-tui/internal/sync"
)

const instructions = `Asana tasks from a local cache that asana-tui keeps in sync.

- Reads (list_tasks, search_tasks, get_task, list_projects) are served from the local cache,
  so they are fast and work offline. Call sync first if you need the very latest data.
- Writes (create_task, update_task, add_comment) are applied to the cache immediately and sent
  to Asana right away when online. If offline, they are queued and sent on the next sync;
  the response says whether the change was sent.
- Task and project arguments accept a gid. Projects also accept a name or a unique part of it.
- Dates are YYYY-MM-DD, or relative: today, tomorrow, yesterday, +7d, -3d.
- Task names and comments are often in Japanese; search_tasks matches substrings.`

// Server holds the dependencies of the MCP tools.
type Server struct {
	Store *store.Store
	// NewSyncer creates an authenticated syncer; nil means offline-only.
	NewSyncer func(ctx context.Context) (*sync.Syncer, error)
	// ReadOnly hides the write tools (create_task, update_task, add_comment).
	ReadOnly bool
	Version  string
	Now      func() time.Time

	mu     stdsync.Mutex
	syncer *sync.Syncer
}

// New builds the MCP server with all tools registered.
func (s *Server) New() *mcp.Server {
	if s.Now == nil {
		s.Now = time.Now
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "asana-tui", Title: "Asana (asana-tui)", Version: s.Version},
		&mcp.ServerOptions{Instructions: instructions})

	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_tasks", Annotations: readOnly,
		Description: "List tasks from the local cache. Defaults to my incomplete tasks, ordered by due date. " +
			"Filter by project/section, assignee, due date range, overdue, tag or completion."}, s.listTasks)
	mcp.AddTool(srv, &mcp.Tool{Name: "search_tasks", Annotations: readOnly,
		Description: "Full-text search over task names, descriptions and comments in every synced project. " +
			"Multiple words are ANDed. Includes completed tasks unless completion is set."}, s.searchTasks)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_task", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Get one task with notes, subtasks and comments. Refreshes it from Asana when online " +
			"(set offline=true to use only the cache)."}, s.getTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_projects", Annotations: readOnly,
		Description: "List projects and their sections (gid and name). By default only projects I am a member of."}, s.listProjects)
	mcp.AddTool(srv, &mcp.Tool{Name: "sync_status", Annotations: readOnly,
		Description: "Show when the cache was last synced and any local changes not yet sent " +
			"(pending, conflicts, failures)."}, s.syncStatus)
	mcp.AddTool(srv, &mcp.Tool{Name: "sync", Annotations: &mcp.ToolAnnotations{IdempotentHint: true, OpenWorldHint: ptr(true)},
		Description: "Send queued local changes to Asana, then pull changes from Asana into the cache " +
			"(incremental; full=true re-fetches everything and removes deleted tasks)."}, s.sync)

	if !s.ReadOnly {
		write := func(destructive bool) *mcp.ToolAnnotations {
			return &mcp.ToolAnnotations{DestructiveHint: ptr(destructive), OpenWorldHint: ptr(true)}
		}
		mcp.AddTool(srv, &mcp.Tool{Name: "create_task", Annotations: write(false),
			Description: "Create a task. Without a project it is assigned to me in the default workspace " +
				"(like My Tasks). With a project it goes to the given section or the project's first section."}, s.createTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "update_task", Annotations: write(true),
			Description: "Update a task: complete/reopen it, rename it, change its description, due date or assignee. " +
				"Only the fields you pass are changed. Use an empty string to clear due_on or assignee."}, s.updateTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "add_comment", Annotations: write(false),
			Description: "Add a comment to a task."}, s.addComment)
	}
	return srv
}

// --- inputs / outputs ---

type TaskFilterInput struct {
	Assignee   string `json:"assignee,omitempty" jsonschema:"\"me\" (default for list_tasks without project), \"any\", or a user gid"`
	Project    string `json:"project,omitempty" jsonschema:"project gid or name (partial match allowed)"`
	Section    string `json:"section,omitempty" jsonschema:"section gid or exact name; requires project"`
	Completion string `json:"completion,omitempty" jsonschema:"\"open\" (default for list_tasks), \"completed\" or \"all\""`
	DueBefore  string `json:"due_before,omitempty" jsonschema:"due on or before this date (YYYY-MM-DD, today, tomorrow, +7d)"`
	DueAfter   string `json:"due_after,omitempty" jsonschema:"due on or after this date"`
	Overdue    bool   `json:"overdue,omitempty" jsonschema:"only incomplete tasks whose due date has passed"`
	Tag        string `json:"tag,omitempty" jsonschema:"tag name or gid"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum number of tasks (default 100)"`
}

type SearchInput struct {
	Query string `json:"query" jsonschema:"words to search for in task names, descriptions and comments"`
	TaskFilterInput
}

type TasksOutput struct {
	Tasks []store.TaskSummary `json:"tasks"`
	Count int                 `json:"count"`
}

type GetTaskInput struct {
	GID     string `json:"gid" jsonschema:"task gid"`
	Offline bool   `json:"offline,omitempty" jsonschema:"do not contact Asana; use only the local cache"`
}

type GetTaskOutput struct {
	Task *store.TaskDetail `json:"task"`
	// Note explains when the cache was used because Asana could not be reached.
	Note string `json:"note,omitempty"`
}

type ListProjectsInput struct {
	All bool `json:"all,omitempty" jsonschema:"include projects I am not a member of"`
}

type ProjectsOutput struct {
	Projects []store.ProjectView `json:"projects"`
}

type SyncInput struct {
	Full bool `json:"full,omitempty" jsonschema:"re-fetch everything and remove tasks deleted in Asana"`
}

type SyncStatusOutput struct {
	LastSync *time.Time `json:"last_sync,omitempty"`
	Tasks    int        `json:"tasks"`
	Open     int        `json:"open_tasks"`
	Pending  int        `json:"pending"`
	Conflict int        `json:"conflicts"`
	Failed   int        `json:"failed"`
	// Outbox lists local changes not yet sent. Conflicts need the user to decide
	// (asana-tui TUI "!" or `asana-tui outbox resolve`).
	Outbox []store.Op `json:"outbox"`
}

type SyncOutput struct {
	Result sync.Result      `json:"result"`
	Status SyncStatusOutput `json:"status"`
}

type CreateTaskInput struct {
	Name     string `json:"name" jsonschema:"task name"`
	Project  string `json:"project,omitempty" jsonschema:"project gid or name; omit to create in My Tasks"`
	Section  string `json:"section,omitempty" jsonschema:"section gid or exact name (default: first section)"`
	DueOn    string `json:"due_on,omitempty" jsonschema:"due date (YYYY-MM-DD, today, tomorrow, +7d)"`
	Assignee string `json:"assignee,omitempty" jsonschema:"\"me\" or a user gid (default: me when no project)"`
	Notes    string `json:"notes,omitempty" jsonschema:"description"`
}

type UpdateTaskInput struct {
	GID       string  `json:"gid" jsonschema:"task gid"`
	Completed *bool   `json:"completed,omitempty" jsonschema:"true to complete, false to reopen"`
	Name      *string `json:"name,omitempty" jsonschema:"new name"`
	Notes     *string `json:"notes,omitempty" jsonschema:"new description (replaces the old one)"`
	DueOn     *string `json:"due_on,omitempty" jsonschema:"new due date (YYYY-MM-DD, today, tomorrow, +7d), or empty string to clear"`
	Assignee  *string `json:"assignee,omitempty" jsonschema:"\"me\", a user gid, or empty string to unassign"`
}

type AddCommentInput struct {
	GID  string `json:"gid" jsonschema:"task gid"`
	Text string `json:"text" jsonschema:"comment text"`
}

type WriteOutput struct {
	TaskGID string `json:"task_gid,omitempty"`
	// Sent is true when the change reached Asana; false means it is queued locally.
	Sent bool   `json:"sent"`
	Note string `json:"note,omitempty"`
}

// --- read tools ---

func (s *Server) listTasks(ctx context.Context, _ *mcp.CallToolRequest, in TaskFilterInput) (*mcp.CallToolResult, TasksOutput, error) {
	f, err := s.filter(ctx, in, "me", ptr(false))
	if err != nil {
		return nil, TasksOutput{}, err
	}
	tasks, err := s.Store.ListTasks(ctx, f)
	return nil, TasksOutput{Tasks: tasks, Count: len(tasks)}, err
}

func (s *Server) searchTasks(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, TasksOutput, error) {
	if in.Query == "" {
		return nil, TasksOutput{}, errors.New("query is required")
	}
	f, err := s.filter(ctx, in.TaskFilterInput, "any", nil)
	if err != nil {
		return nil, TasksOutput{}, err
	}
	f.Query = in.Query
	tasks, err := s.Store.ListTasks(ctx, f)
	return nil, TasksOutput{Tasks: tasks, Count: len(tasks)}, err
}

func (s *Server) filter(ctx context.Context, in TaskFilterInput, defaultAssignee string, defaultCompleted *bool) (store.TaskFilter, error) {
	now := s.Now()
	f := store.TaskFilter{Tag: in.Tag, Limit: in.Limit, Completed: defaultCompleted}
	if f.Limit <= 0 {
		f.Limit = 100
	}
	assignee := in.Assignee
	if assignee == "" {
		assignee = defaultAssignee
		if in.Project != "" {
			assignee = "any"
		}
	}
	switch assignee {
	case "any", "all":
	case "me":
		me, err := s.Store.MeGID(ctx)
		if err != nil {
			return f, err
		}
		f.AssigneeGID = me
	default:
		f.AssigneeGID = assignee
	}
	if in.Project != "" {
		gid, err := s.Store.ResolveProject(ctx, in.Project)
		if err != nil {
			return f, err
		}
		f.ProjectGID = gid
		if in.Section != "" {
			if f.SectionGID, err = s.Store.ResolveSection(ctx, gid, in.Section); err != nil {
				return f, err
			}
		}
	} else if in.Section != "" {
		return f, errors.New("section requires project")
	}
	var err error
	if f.DueBefore, err = dates.Parse(in.DueBefore, now); err != nil {
		return f, fmt.Errorf("due_before: %w", err)
	}
	if f.DueAfter, err = dates.Parse(in.DueAfter, now); err != nil {
		return f, fmt.Errorf("due_after: %w", err)
	}
	switch in.Completion {
	case "":
	case "open":
		f.Completed = ptr(false)
	case "completed":
		f.Completed = ptr(true)
	case "all":
		f.Completed = nil
	default:
		return f, fmt.Errorf("completion must be open, completed or all (got %q)", in.Completion)
	}
	if in.Overdue {
		f.DueBefore = now.AddDate(0, 0, -1).Format(time.DateOnly)
		f.Completed = ptr(false)
	}
	return f, nil
}

func (s *Server) getTask(ctx context.Context, _ *mcp.CallToolRequest, in GetTaskInput) (*mcp.CallToolResult, GetTaskOutput, error) {
	var out GetTaskOutput
	if !in.Offline {
		if err := s.fetchDetails(ctx, in.GID); err != nil {
			out.Note = "Asana could not be reached; showing the local cache: " + err.Error()
		}
	}
	d, err := s.Store.GetTask(ctx, in.GID)
	if err != nil {
		return nil, out, err
	}
	out.Task = d
	return nil, out, nil
}

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, ProjectsOutput, error) {
	ps, err := s.Store.ListProjects(ctx, !in.All)
	return nil, ProjectsOutput{Projects: ps}, err
}

func (s *Server) syncStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, SyncStatusOutput, error) {
	out, err := s.status(ctx)
	return nil, out, err
}

func (s *Server) status(ctx context.Context) (SyncStatusOutput, error) {
	var out SyncStatusOutput
	stats, err := s.Store.Stats(ctx)
	if err != nil {
		return out, err
	}
	out.LastSync, out.Tasks, out.Open = stats.LastSync, stats.Tasks, stats.OpenTasks
	if out.Pending, out.Conflict, out.Failed, err = s.Store.OutboxCounts(ctx); err != nil {
		return out, err
	}
	out.Outbox, err = s.Store.ListOps(ctx)
	return out, err
}

func (s *Server) sync(ctx context.Context, _ *mcp.CallToolRequest, in SyncInput) (*mcp.CallToolResult, SyncOutput, error) {
	var out SyncOutput
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return nil, out, err
	}
	if out.Result, err = syncer.Run(ctx, in.Full); err != nil {
		return nil, out, err
	}
	out.Status, err = s.status(ctx)
	return nil, out, err
}

// --- write tools ---

func (s *Server) createTask(ctx context.Context, _ *mcp.CallToolRequest, in CreateTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	now := s.Now()
	nt := store.NewTask{Name: in.Name, Notes: in.Notes}
	var err error
	if nt.DueOn, err = dates.Parse(in.DueOn, now); err != nil {
		return nil, WriteOutput{}, fmt.Errorf("due_on: %w", err)
	}
	assignee := in.Assignee
	if in.Project != "" {
		if nt.ProjectGID, err = s.Store.ResolveProject(ctx, in.Project); err != nil {
			return nil, WriteOutput{}, err
		}
		if in.Section != "" {
			nt.SectionGID, err = s.Store.ResolveSection(ctx, nt.ProjectGID, in.Section)
		} else {
			nt.SectionGID, err = s.Store.FirstSection(ctx, nt.ProjectGID)
		}
		if err != nil {
			return nil, WriteOutput{}, err
		}
	} else {
		if nt.WorkspaceGID, err = s.Store.DefaultWorkspace(ctx); err != nil {
			return nil, WriteOutput{}, err
		}
		if assignee == "" {
			assignee = "me"
		}
	}
	if nt.AssigneeGID, err = s.resolveAssignee(ctx, assignee); err != nil {
		return nil, WriteOutput{}, err
	}
	gid, err := s.Store.CreateTaskLocal(ctx, nt, now)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	out := s.push(ctx, gid)
	return nil, out, nil
}

func (s *Server) updateTask(ctx context.Context, _ *mcp.CallToolRequest, in UpdateTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	changes := map[string]any{}
	if in.Completed != nil {
		changes["completed"] = *in.Completed
	}
	if in.Name != nil {
		changes["name"] = *in.Name
	}
	if in.Notes != nil {
		changes["notes"] = *in.Notes
	}
	if in.DueOn != nil {
		due, err := dates.Parse(*in.DueOn, s.Now())
		if err != nil {
			return nil, WriteOutput{}, fmt.Errorf("due_on: %w", err)
		}
		changes["due_on"] = due
	}
	if in.Assignee != nil {
		gid, err := s.resolveAssignee(ctx, *in.Assignee)
		if err != nil {
			return nil, WriteOutput{}, err
		}
		changes["assignee"] = gid
	}
	if len(changes) == 0 {
		return nil, WriteOutput{}, errors.New("nothing to update: pass at least one of completed, name, notes, due_on, assignee")
	}
	if err := s.Store.UpdateTaskLocal(ctx, in.GID, changes, s.Now()); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
}

func (s *Server) addComment(ctx context.Context, _ *mcp.CallToolRequest, in AddCommentInput) (*mcp.CallToolResult, WriteOutput, error) {
	me, err := s.Store.Me(ctx)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	if _, err := s.Store.AddCommentLocal(ctx, in.GID, in.Text, me, s.Now()); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
}

func (s *Server) resolveAssignee(ctx context.Context, a string) (string, error) {
	if a == "me" {
		return s.Store.MeGID(ctx)
	}
	return a, nil
}

// push sends the outbox now. A failure is reported in the output, not as a tool error:
// the change is saved and will be sent by the next sync.
func (s *Server) push(ctx context.Context, taskGID string) WriteOutput {
	out := WriteOutput{TaskGID: taskGID}
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		out.Note = "saved locally; will be sent on the next sync (" + err.Error() + ")"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := syncer.Push(ctx)
	switch {
	case err != nil:
		out.Note = "saved locally; Asana could not be reached, will be sent on the next sync (" + err.Error() + ")"
	case res.Conflicts > 0 || res.Failed > 0:
		out.Note = fmt.Sprintf("%d conflict(s) / %d failure(s) in the outbox; see sync_status", res.Conflicts, res.Failed)
	default:
		out.Sent = true
	}
	if real, err := s.Store.RealGID(ctx, taskGID); err == nil {
		out.TaskGID = real
	}
	return out
}

func (s *Server) fetchDetails(ctx context.Context, gid string) error {
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return syncer.FetchDetails(ctx, gid)
}

// getSyncer creates the syncer once; on failure it is retried on the next call.
func (s *Server) getSyncer(ctx context.Context) (*sync.Syncer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.syncer != nil {
		return s.syncer, nil
	}
	if s.NewSyncer == nil {
		return nil, errors.New("offline mode")
	}
	syncer, err := s.NewSyncer(ctx)
	if err != nil {
		return nil, err
	}
	s.syncer = syncer
	return syncer, nil
}

func ptr[T any](v T) *T { return &v }
