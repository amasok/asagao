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
	"net/http"
	"os"
	"strings"
	stdsync "sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asana-tui/internal/dates"
	"github.com/amasok/asana-tui/internal/richtext"
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
	// ReadOnly hides the write tools (create_task, update_task, add_comment, ...).
	ReadOnly bool
	// CacheDir is where files to upload are copied before sending.
	CacheDir string
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
		Description: "Get one task with its description and comments as Markdown, subtasks, tags and attachments. " +
			"Refreshes it from Asana when online (set offline=true to use only the cache)."}, s.getTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_attachment", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Get an attachment of a task. Images are returned as image content so you can look at them " +
			"(e.g. those referenced as ![画像](asana:<gid>) in a description); other files return their local path."}, s.getAttachment)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_projects", Annotations: readOnly,
		Description: "List projects and their sections (gid and name). By default only projects I am a member of."}, s.listProjects)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_users", Annotations: readOnly,
		Description: "List workspace members (gid and name), e.g. to choose an assignee."}, s.listUsers)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_project", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Get a project: sections, members and its status updates (Markdown, newest first)."}, s.getProject)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_tags", Annotations: readOnly,
		Description: "List tags used by tasks, with the number of open and total tasks for each."}, s.listTags)
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
				"(like My Tasks). With a project it goes to the given section or the project's first section. " +
				"With parent it is created as a subtask of that task."}, s.createTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "update_task", Annotations: write(true),
			Description: "Update a task: complete/reopen it, rename it, change its description (Markdown), due date, " +
				"assignee, tags, followers, likes or the projects it is in. Only the fields you pass are changed. " +
				"Use an empty string to clear due_on or assignee."}, s.updateTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "move_task", Annotations: write(false),
			Description: "Move a task to another section of its project (e.g. from \"Ready\" to \"Done\"). " +
				"project is needed only when the task is in more than one project."}, s.moveTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "rename_section", Annotations: write(false),
			Description: "Rename a section (a list/column) of a project."}, s.renameSection)
		mcp.AddTool(srv, &mcp.Tool{Name: "duplicate_task", Annotations: write(false),
			Description: "Duplicate a task (with its description, assignee, dates, projects, tags, subtasks, attachments and followers)."}, s.duplicateTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "upload_attachment", Annotations: write(false),
			Description: "Attach a local file to a task (max 100MB)."}, s.uploadAttachment)
		mcp.AddTool(srv, &mcp.Tool{Name: "create_status_update", Annotations: write(false),
			Description: "Post a status update on a project: status (on_track, at_risk, off_track, on_hold, complete), title and a Markdown text."}, s.createStatus)
		mcp.AddTool(srv, &mcp.Tool{Name: "create_project", Annotations: write(false),
			Description: "Create a project (needs Asana to be reachable)."}, s.createProject)
		mcp.AddTool(srv, &mcp.Tool{Name: "update_project", Annotations: write(true),
			Description: "Archive / unarchive a project or add / remove its members (needs Asana to be reachable). Ask before archiving."}, s.updateProject)
		mcp.AddTool(srv, &mcp.Tool{Name: "resolve_outbox", Annotations: write(true),
			Description: "Resolve a local change that could not be sent (see sync_status.outbox): keep_local overwrites " +
				"Asana with the local value, keep_remote drops the local change, retry resends a failed entry, discard drops it. " +
				"Ask the user before choosing for a conflict."}, s.resolveOutbox)
		mcp.AddTool(srv, &mcp.Tool{Name: "add_comment", Annotations: write(false),
			Description: "Add a comment to a task."}, s.addComment)
	}
	return srv
}

// --- inputs / outputs ---

type TaskFilterInput struct {
	Assignee   string `json:"assignee,omitempty" jsonschema:"\"me\" (default for list_tasks without project), \"any\", or a user gid or name"`
	Project    string `json:"project,omitempty" jsonschema:"project gid or name (partial match allowed)"`
	Section    string `json:"section,omitempty" jsonschema:"section gid or exact name; requires project"`
	Completion string `json:"completion,omitempty" jsonschema:"\"open\" (default for list_tasks), \"completed\" or \"all\""`
	DueBefore  string `json:"due_before,omitempty" jsonschema:"due on or before this date (YYYY-MM-DD, today, tomorrow, +7d)"`
	DueAfter   string `json:"due_after,omitempty" jsonschema:"due on or after this date"`
	Overdue    bool   `json:"overdue,omitempty" jsonschema:"only incomplete tasks whose due date has passed"`
	Tag        string `json:"tag,omitempty" jsonschema:"tag name or gid"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum number of tasks (default 100)"`
	// recent updates (an inbox substitute)
	ModifiedWithinDays int `json:"modified_within_days,omitempty" jsonschema:"only tasks modified in the last N days, newest first (what changed recently)"`
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
	// DescriptionMarkdown is the description as Markdown (images as ![画像](asana:<gid>),
	// readable with get_attachment).
	DescriptionMarkdown string `json:"description_markdown"`
	// Comments are the task's comments as Markdown, oldest first.
	Comments []CommentOutput `json:"comments"`
	// Note explains when the cache was used because Asana could not be reached.
	Note string `json:"note,omitempty"`
}

type CommentOutput struct {
	GID       string `json:"gid"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Markdown  string `json:"markdown"`
	Pending   bool   `json:"pending"`
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
	Name     string   `json:"name" jsonschema:"task name"`
	Parent   string   `json:"parent,omitempty" jsonschema:"parent task gid: create a subtask (project/section are ignored)"`
	Project  string   `json:"project,omitempty" jsonschema:"project gid or name; omit to create in My Tasks"`
	Section  string   `json:"section,omitempty" jsonschema:"section gid or exact name (default: first section)"`
	DueOn    string   `json:"due_on,omitempty" jsonschema:"due date (YYYY-MM-DD, today, tomorrow, +7d)"`
	Assignee string   `json:"assignee,omitempty" jsonschema:"\"me\", \"none\", a user gid or name (default: me when no project)"`
	Notes    string   `json:"notes,omitempty" jsonschema:"description in Markdown (headings, **bold**, lists, links); converted to Asana rich text"`
	Tags     []string `json:"tags,omitempty" jsonschema:"tag names (created if they do not exist) or gids"`
}

type UpdateTaskInput struct {
	GID             string   `json:"gid" jsonschema:"task gid"`
	Completed       *bool    `json:"completed,omitempty" jsonschema:"true to complete, false to reopen"`
	Name            *string  `json:"name,omitempty" jsonschema:"new name"`
	Notes           *string  `json:"notes,omitempty" jsonschema:"new description in Markdown, replacing the old one (keep ![画像](asana:<gid>) lines to keep images)"`
	DueOn           *string  `json:"due_on,omitempty" jsonschema:"new due date (YYYY-MM-DD, today, tomorrow, +7d), or empty string to clear"`
	Assignee        *string  `json:"assignee,omitempty" jsonschema:"\"me\", a user gid or name (see list_users), or empty string to unassign"`
	AddTags         []string `json:"add_tags,omitempty" jsonschema:"tag names (created if they do not exist) or gids to add"`
	RemoveTags      []string `json:"remove_tags,omitempty" jsonschema:"tag names or gids to remove"`
	Liked           *bool    `json:"liked,omitempty" jsonschema:"true to like the task, false to unlike"`
	AddFollowers    []string `json:"add_followers,omitempty" jsonschema:"\"me\", user names or gids to add as followers"`
	RemoveFollowers []string `json:"remove_followers,omitempty" jsonschema:"\"me\", user names or gids to remove from followers"`
	AddProjects     []string `json:"add_projects,omitempty" jsonschema:"project names or gids to also put the task in (first section)"`
	RemoveProjects  []string `json:"remove_projects,omitempty" jsonschema:"project names or gids to take the task out of"`
}

type DuplicateTaskInput struct {
	GID  string `json:"gid" jsonschema:"task gid to duplicate"`
	Name string `json:"name,omitempty" jsonschema:"name of the copy (default: \"<name> のコピー\")"`
}

type UploadAttachmentInput struct {
	TaskGID string `json:"task_gid" jsonschema:"task to attach the file to"`
	Path    string `json:"path" jsonschema:"absolute path of a local file (max 100MB)"`
}

type MoveTaskInput struct {
	GID     string `json:"gid" jsonschema:"task gid"`
	Section string `json:"section" jsonschema:"destination section gid or exact name"`
	Project string `json:"project,omitempty" jsonschema:"project gid or name; only needed if the task is in several projects"`
}

type RenameSectionInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
	Section string `json:"section" jsonschema:"section gid or current exact name"`
	Name    string `json:"name" jsonschema:"new section name"`
}

type TagsOutput struct {
	Tags []store.TagCount `json:"tags"`
}

type AddCommentInput struct {
	GID  string `json:"gid" jsonschema:"task gid"`
	Text string `json:"text" jsonschema:"comment in Markdown (converted to Asana rich text)"`
}

type ResolveOutboxInput struct {
	ID     int64  `json:"id" jsonschema:"outbox entry id (from sync_status)"`
	Action string `json:"action" jsonschema:"keep_local (overwrite Asana with the local value), keep_remote (drop the local change), retry (send a failed entry again) or discard (drop it)"`
}

type GetAttachmentInput struct {
	GID     string `json:"gid" jsonschema:"attachment gid (e.g. from ![画像](asana:<gid>) or task.attachments)"`
	TaskGID string `json:"task_gid" jsonschema:"the task it belongs to"`
}

type AttachmentOutput struct {
	Name      string `json:"name"`
	LocalPath string `json:"local_path"`
	IsImage   bool   `json:"is_image"`
	Note      string `json:"note,omitempty"`
}

type WriteOutput struct {
	TaskGID string `json:"task_gid,omitempty"`
	// Sent is true when the change reached Asana; false means it is queued locally.
	Sent bool   `json:"sent"`
	Note string `json:"note,omitempty"`
	// Discarded lists queued changes dropped because what they change was deleted in Asana.
	Discarded []store.Discard `json:"discarded,omitempty"`
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
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	return s.Store.BuildFilter(ctx, store.FilterSpec{
		Assignee: in.Assignee, DefaultAssignee: defaultAssignee,
		Project: in.Project, Section: in.Section, Tag: in.Tag,
		DueBefore: in.DueBefore, DueAfter: in.DueAfter,
		Completion: in.Completion, DefaultCompleted: defaultCompleted,
		Overdue: in.Overdue, ModifiedWithinDays: in.ModifiedWithinDays, Limit: limit,
	}, s.Now())
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
	out.DescriptionMarkdown = editableMarkdown(d.HTMLNotes, d.Notes)
	out.Comments = []CommentOutput{}
	for _, st := range d.Stories {
		if st.Type != "comment" {
			continue
		}
		c := CommentOutput{GID: st.GID, CreatedAt: st.CreatedAt, Markdown: editableMarkdown(st.HTMLText, st.Text), Pending: st.Pending}
		if st.CreatedBy != nil {
			c.Author = st.CreatedBy.Name
		}
		out.Comments = append(out.Comments, c)
	}
	return nil, out, nil
}

// editableMarkdown renders rich text (or plain text) as Markdown that update_task accepts back.
func editableMarkdown(htmlText, plain string) string {
	if htmlText != "" {
		return richtext.ToEditableMarkdown(htmlText)
	}
	md := richtext.FromPlain(store.ReplaceInlineAssets(plain, func(gid string) string {
		return "\n\n" + richtext.ImageToken(gid) + "\n\n"
	}))
	return richtext.ImageTokenRe.ReplaceAllString(md, "![画像](asana:$1)")
}

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, ProjectsOutput, error) {
	ps, err := s.Store.ListProjects(ctx, !in.All)
	return nil, ProjectsOutput{Projects: ps}, err
}

type UsersOutput struct {
	Users []store.UserRef `json:"users"`
}

func (s *Server) listUsers(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, UsersOutput, error) {
	users, err := s.Store.ListUsers(ctx)
	return nil, UsersOutput{Users: users}, err
}

func (s *Server) listTags(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, TagsOutput, error) {
	tags, err := s.Store.ListTags(ctx)
	return nil, TagsOutput{Tags: tags}, err
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
	nt := store.NewTask{Name: in.Name, ParentGID: in.Parent}
	if in.Notes != "" {
		nt.Notes, nt.HTMLNotes = richtext.PlainFromMarkdown(in.Notes), richtext.FromMarkdown(in.Notes)
	}
	var err error
	if nt.DueOn, err = dates.Parse(in.DueOn, now); err != nil {
		return nil, WriteOutput{}, fmt.Errorf("due_on: %w", err)
	}
	assignee := in.Assignee
	if in.Parent != "" {
		// subtask: workspace comes from the parent
	} else if in.Project != "" {
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
	if nt.AssigneeGID, err = s.Store.ResolveUser(ctx, assignee); err != nil {
		return nil, WriteOutput{}, err
	}
	tags, err := s.Store.ResolveTagEdit(ctx, in.Tags, nil)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	gid, err := s.Store.CreateTaskLocal(ctx, nt, now)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	if err := s.Store.ApplyTagEdit(ctx, gid, tags, now); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, gid), nil
}

// updateTask resolves and checks all of its input before writing anything, so that a bad
// name (tag, follower, project) does not leave half of the update saved.
func (s *Server) updateTask(ctx context.Context, _ *mcp.CallToolRequest, in UpdateTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	now := s.Now()
	changes := map[string]any{}
	if in.Completed != nil {
		changes["completed"] = *in.Completed
	}
	if in.Name != nil {
		changes["name"] = *in.Name
	}
	if in.Notes != nil {
		changes["notes"] = richtext.PlainFromMarkdown(*in.Notes)
		changes["html_notes"] = richtext.FromMarkdown(*in.Notes)
	}
	if in.Liked != nil {
		changes["liked"] = *in.Liked
	}
	if in.DueOn != nil {
		due, err := dates.Parse(*in.DueOn, now)
		if err != nil {
			return nil, WriteOutput{}, fmt.Errorf("due_on: %w", err)
		}
		changes["due_on"] = due
	}
	if in.Assignee != nil {
		gid, err := s.Store.ResolveUser(ctx, *in.Assignee)
		if err != nil {
			return nil, WriteOutput{}, err
		}
		changes["assignee"] = gid
	}
	tags, err := s.Store.ResolveTagEdit(ctx, in.AddTags, in.RemoveTags)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	addFollowers, err := s.Store.ResolveUsers(ctx, in.AddFollowers)
	if err != nil {
		return nil, WriteOutput{}, fmt.Errorf("add_followers: %w", err)
	}
	removeFollowers, err := s.Store.ResolveUsers(ctx, in.RemoveFollowers)
	if err != nil {
		return nil, WriteOutput{}, fmt.Errorf("remove_followers: %w", err)
	}
	addProjects, err := s.Store.ResolveProjects(ctx, in.AddProjects)
	if err != nil {
		return nil, WriteOutput{}, fmt.Errorf("add_projects: %w", err)
	}
	removeProjects, err := s.Store.ResolveProjects(ctx, in.RemoveProjects)
	if err != nil {
		return nil, WriteOutput{}, fmt.Errorf("remove_projects: %w", err)
	}
	if len(changes) == 0 && tags.Empty() && len(addFollowers)+len(removeFollowers)+len(addProjects)+len(removeProjects) == 0 {
		return nil, WriteOutput{}, errors.New("nothing to update: pass at least one field to change")
	}
	if _, err := s.Store.GetTask(ctx, in.GID); err != nil {
		return nil, WriteOutput{}, err
	}

	if len(changes) > 0 {
		if err := s.Store.UpdateTaskLocal(ctx, in.GID, changes, now); err != nil {
			return nil, WriteOutput{}, err
		}
	}
	if err := s.Store.ApplyTagEdit(ctx, in.GID, tags, now); err != nil {
		return nil, WriteOutput{}, err
	}
	for _, f := range []struct {
		users  []string
		follow bool
	}{{addFollowers, true}, {removeFollowers, false}} {
		for _, u := range f.users {
			if err := s.Store.SetFollowerLocal(ctx, in.GID, u, f.follow, now); err != nil {
				return nil, WriteOutput{}, err
			}
		}
	}
	for _, p := range []struct {
		projects []string
		add      bool
	}{{addProjects, true}, {removeProjects, false}} {
		for _, gid := range p.projects {
			if err := s.Store.SetProjectLocal(ctx, in.GID, gid, "", p.add, now); err != nil {
				return nil, WriteOutput{}, err
			}
		}
	}
	return nil, s.push(ctx, in.GID), nil
}

func (s *Server) moveTask(ctx context.Context, _ *mcp.CallToolRequest, in MoveTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	projectGID, sectionGID, err := s.Store.ResolveMove(ctx, in.GID, in.Project, in.Section)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	if err := s.Store.MoveTaskLocal(ctx, in.GID, projectGID, sectionGID, s.Now()); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
}

func (s *Server) renameSection(ctx context.Context, _ *mcp.CallToolRequest, in RenameSectionInput) (*mcp.CallToolResult, WriteOutput, error) {
	projectGID, err := s.Store.ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	gid, err := s.Store.ResolveSection(ctx, projectGID, in.Section)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	if err := s.Store.RenameSectionLocal(ctx, gid, in.Name, s.Now()); err != nil {
		return nil, WriteOutput{}, err
	}
	out := s.push(ctx, "")
	return nil, out, nil
}

func (s *Server) addComment(ctx context.Context, _ *mcp.CallToolRequest, in AddCommentInput) (*mcp.CallToolResult, WriteOutput, error) {
	me, err := s.Store.Me(ctx)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	if _, err := s.Store.AddRichCommentLocal(ctx, in.GID, richtext.PlainFromMarkdown(in.Text), richtext.FromMarkdown(in.Text), me, s.Now()); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
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
	out.Discarded = res.Discarded
	if len(res.Discarded) > 0 {
		out.Note = fmt.Sprintf("%d change(s) to items deleted in Asana were discarded; ", len(res.Discarded))
	}
	switch {
	case errors.Is(err, sync.ErrPushBusy):
		out.Note += "saved locally; another asana-tui process is sending the outbox right now and will send this change too"
	case err != nil:
		out.Note += "saved locally; Asana could not be reached, will be sent on the next sync (" + err.Error() + ")"
	case res.Conflicts > 0 || res.Failed > 0:
		out.Note += fmt.Sprintf("%d conflict(s) / %d failure(s) in the outbox; see sync_status", res.Conflicts, res.Failed)
	default:
		out.Sent = true
	}
	if real, err := s.Store.RealGID(ctx, taskGID); err == nil {
		out.TaskGID = real
	}
	for _, d := range res.Discarded {
		if d.TaskGID == taskGID || d.TaskGID == out.TaskGID {
			out.Sent = false // this change was dropped, not sent
		}
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

func (s *Server) resolveOutbox(ctx context.Context, _ *mcp.CallToolRequest, in ResolveOutboxInput) (*mcp.CallToolResult, WriteOutput, error) {
	action, err := sync.ParseResolveAction(in.Action)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	r, err := sync.Resolve(ctx, s.Store, in.ID, action)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	if r.Push {
		return nil, s.push(ctx, r.Op.TaskGID), nil
	}
	out := WriteOutput{TaskGID: r.Op.TaskGID, Sent: true, Note: "local change dropped"}
	if r.RefreshGID != "" {
		if err := s.fetchDetails(ctx, r.RefreshGID); err != nil {
			out.Note += "; Asana's values will be loaded on the next sync (" + err.Error() + ")"
		}
	}
	return nil, out, nil
}

// maxImageBytes keeps images returned to the model reasonably small.
const maxImageBytes = 4 << 20

func (s *Server) getAttachment(ctx context.Context, _ *mcp.CallToolRequest, in GetAttachmentInput) (*mcp.CallToolResult, AttachmentOutput, error) {
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		syncer = &sync.Syncer{Store: s.Store} // offline: cached files only
	}
	path, err := syncer.DownloadAttachment(ctx, in.TaskGID, in.GID)
	if err != nil {
		return nil, AttachmentOutput{}, err
	}
	a, err := s.Store.GetAttachment(ctx, in.GID)
	if err != nil {
		return nil, AttachmentOutput{}, err
	}
	out := AttachmentOutput{Name: a.Name, LocalPath: path, IsImage: a.IsImage}
	if !a.IsImage {
		return nil, out, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, out, err
	}
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") || len(data) > maxImageBytes {
		out.Note = "not returned inline (" + mime + ", " + fmt.Sprint(len(data)) + " bytes); open local_path instead"
		return nil, out, nil
	}
	res := &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: data, MIMEType: mime},
		&mcp.TextContent{Text: a.Name + " (" + path + ")"},
	}}
	return res, out, nil
}

func (s *Server) duplicateTask(ctx context.Context, _ *mcp.CallToolRequest, in DuplicateTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	gid, err := s.Store.DuplicateTaskLocal(ctx, in.GID, in.Name, s.Now())
	if err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, gid), nil
}

func (s *Server) uploadAttachment(ctx context.Context, _ *mcp.CallToolRequest, in UploadAttachmentInput) (*mcp.CallToolResult, WriteOutput, error) {
	if _, err := s.Store.UploadAttachmentLocal(ctx, in.TaskGID, in.Path, s.CacheDir, s.Now()); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.TaskGID), nil
}

type ProjectInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
}

type ProjectOutput struct {
	Project  store.ProjectView `json:"project"`
	Members  []store.UserRef   `json:"members"`
	Statuses []StatusOutput    `json:"status_updates"`
	Note     string            `json:"note,omitempty"`
}

type StatusOutput struct {
	GID       string `json:"gid"`
	Status    string `json:"status"`
	Title     string `json:"title"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	Markdown  string `json:"markdown"`
	Pending   bool   `json:"pending"`
}

func (s *Server) getProject(ctx context.Context, _ *mcp.CallToolRequest, in ProjectInput) (*mcp.CallToolResult, ProjectOutput, error) {
	var out ProjectOutput
	gid, err := s.Store.ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, out, err
	}
	if syncer, err := s.getSyncer(ctx); err == nil {
		if err := syncer.FetchStatusUpdates(ctx, gid); err != nil {
			out.Note = "status updates from the local cache: " + err.Error()
		}
	}
	projects, err := s.Store.ListProjects(ctx, false)
	if err != nil {
		return nil, out, err
	}
	for _, p := range projects {
		if p.GID == gid {
			out.Project = p
		}
	}
	if out.Members, err = s.Store.ProjectMembers(ctx, gid); err != nil {
		return nil, out, err
	}
	updates, err := s.Store.StatusUpdates(ctx, gid)
	if err != nil {
		return nil, out, err
	}
	out.Statuses = []StatusOutput{}
	for _, u := range updates {
		out.Statuses = append(out.Statuses, StatusOutput{GID: u.GID, Status: u.StatusType, Title: u.Title, Author: u.Author,
			CreatedAt: u.CreatedAt, Markdown: editableMarkdown(u.HTMLText, u.Text), Pending: u.Pending})
	}
	return nil, out, nil
}

type CreateStatusInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
	Status  string `json:"status" jsonschema:"on_track, at_risk, off_track, on_hold or complete"`
	Title   string `json:"title,omitempty" jsonschema:"short title"`
	Text    string `json:"text" jsonschema:"the update in Markdown"`
}

func (s *Server) createStatus(ctx context.Context, _ *mcp.CallToolRequest, in CreateStatusInput) (*mcp.CallToolResult, WriteOutput, error) {
	gid, err := s.Store.ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	me, err := s.Store.Me(ctx)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	if _, err := s.Store.CreateStatusLocal(ctx, gid, in.Status, in.Title, richtext.PlainFromMarkdown(in.Text), richtext.FromMarkdown(in.Text), me, s.Now()); err != nil {
		return nil, WriteOutput{}, err
	}
	out := s.push(ctx, "")
	return nil, out, nil
}

type CreateProjectInput struct {
	Name string `json:"name" jsonschema:"project name"`
}

type ProjectWriteOutput struct {
	ProjectGID string `json:"project_gid"`
	Note       string `json:"note,omitempty"`
}

func (s *Server) createProject(ctx context.Context, _ *mcp.CallToolRequest, in CreateProjectInput) (*mcp.CallToolResult, ProjectWriteOutput, error) {
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("Asana に接続できないため作れません: %w", err)
	}
	p, err := syncer.CreateProject(ctx, in.Name)
	if err != nil {
		return nil, ProjectWriteOutput{}, err
	}
	return nil, ProjectWriteOutput{ProjectGID: p.GID}, nil
}

type UpdateProjectInput struct {
	Project       string   `json:"project" jsonschema:"project gid or name"`
	Archived      *bool    `json:"archived,omitempty" jsonschema:"true to archive, false to unarchive"`
	AddMembers    []string `json:"add_members,omitempty" jsonschema:"\"me\", user names or gids to add"`
	RemoveMembers []string `json:"remove_members,omitempty" jsonschema:"\"me\", user names or gids to remove"`
}

// updateProject checks all of its input before changing anything in Asana.
func (s *Server) updateProject(ctx context.Context, _ *mcp.CallToolRequest, in UpdateProjectInput) (*mcp.CallToolResult, ProjectWriteOutput, error) {
	gid, err := s.Store.ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, ProjectWriteOutput{}, err
	}
	add, err := s.Store.ResolveUsers(ctx, in.AddMembers)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("add_members: %w", err)
	}
	remove, err := s.Store.ResolveUsers(ctx, in.RemoveMembers)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("remove_members: %w", err)
	}
	members := len(add)+len(remove) > 0
	if !members && in.Archived == nil {
		return nil, ProjectWriteOutput{}, errors.New("nothing to update: pass archived, add_members or remove_members")
	}
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("Asana に接続できないため変更できません: %w", err)
	}
	type step struct {
		name string
		run  func() error
	}
	var steps []step
	if members {
		steps = append(steps, step{"members", func() error { return syncer.SetProjectMembers(ctx, gid, add, remove) }})
	}
	if in.Archived != nil {
		archive := step{"archive", func() error { return syncer.SetProjectArchived(ctx, gid, *in.Archived) }}
		if *in.Archived {
			steps = append(steps, archive) // change members before archiving
		} else {
			archive.name = "unarchive"
			steps = append([]step{archive}, steps...) // and after unarchiving
		}
	}
	for i, st := range steps {
		if err := st.run(); err != nil {
			if i > 0 {
				return nil, ProjectWriteOutput{}, fmt.Errorf("%s was applied, but %s failed: %w", steps[0].name, st.name, err)
			}
			return nil, ProjectWriteOutput{}, err
		}
	}
	return nil, ProjectWriteOutput{ProjectGID: gid}, nil
}
