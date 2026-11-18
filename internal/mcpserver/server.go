// Package mcpserver exposes asagao's local store and outbox as an MCP server.
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
	"path/filepath"
	"strings"
	stdsync "sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/auth"
	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/sync"
)

const instructions = `Asana tasks from a local cache that asagao keeps in sync.

- Reads (list_tasks, search_tasks, get_task, list_projects) are served from the local cache,
  so they are fast and work offline. Call sync first if you need the very latest data.
- Writes (create_task, update_task, add_comment) are applied to the cache immediately and sent
  to Asana right away when online. If offline, they are queued and sent on the next sync;
  the response says whether the change was sent.
- Task and project arguments accept a gid. Projects also accept a name or a unique part of it.
- Dates are YYYY-MM-DD, or relative: today, tomorrow, yesterday, +7d, -3d.
- Task names and comments are often in Japanese; search_tasks matches substrings.
  search_tasks with remote=true also searches Asana, for tasks the cache lacks (projects that
  are not synced, old completed tasks); what it finds is cached.
- list_saved_searches and run_saved_search use the searches the user saved (asagao searches save).
- list_inbox shows my Asana inbox notifications (what others did on my tasks); mark_notifications marks them read.
- get_context returns the project and task of the repository the server runs in (.asana.json), if it has one.
  Prompts standup, triage_overdue and weekly_review are the routine requests.`

// Server holds the dependencies of the MCP tools.
type Server struct {
	Store Store
	// NewSyncer creates an authenticated syncer; nil means offline-only.
	NewSyncer func(ctx context.Context) (*sync.Syncer, error)
	// ReadOnly hides the write tools (create_task, update_task, add_comment, ...).
	ReadOnly bool
	// CacheDir is where files to upload are copied before sending.
	CacheDir string
	// Upload limits which files upload_attachment may send (nothing when empty).
	Upload UploadPolicy
	// AuthScopes is config auth.scopes (to explain how to enable the inbox).
	AuthScopes []string
	// ContextDir is the folder .asana.json is looked for from (get_context and the prompts);
	// "" means none is used.
	ContextDir string
	// Searches is searches.toml, the saved searches (read only; nil: none).
	Searches app.SearchFile
	Version  string
	Now      func() time.Time

	mu     stdsync.Mutex
	syncer *sync.Syncer
}

// query reads the local cache (app.Query over the store).
func (s *Server) query() app.Query { return app.Query{Store: s.Store} }

// New builds the MCP server with all tools registered.
func (s *Server) New() *mcp.Server {
	if s.Now == nil {
		s.Now = time.Now
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "asagao", Title: "Asana (asagao)", Version: s.Version},
		&mcp.ServerOptions{Instructions: instructions})

	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_tasks", Annotations: readOnly,
		Description: "List tasks from the local cache. Defaults to my incomplete tasks, ordered by due date. " +
			"Filter by project/section, assignee, due date range, overdue, tag or completion."}, s.listTasks)
	mcp.AddTool(srv, &mcp.Tool{Name: "search_tasks", Annotations: readOnly,
		Description: "Full-text search over task names, descriptions and comments in every synced project. " +
			"Multiple words are ANDed. Includes completed tasks unless completion is set. " +
			"Set remote=true to also search Asana for tasks the cache lacks (they are cached; " +
			"without Asana's premium search, Asana matches the start of task names only)."}, s.searchTasks)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_task", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Get one task with its description and comments as Markdown, subtasks, tags and attachments. " +
			"Refreshes it from Asana when online (set offline=true to use only the cache)."}, s.getTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_attachment", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Get an attachment of a task. Images are returned as image content so you can look at them " +
			"(e.g. those referenced as ![画像](asana:<gid>) in a description); other files return their local path."}, s.getAttachment)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_projects", Annotations: readOnly,
		Description: "List projects and their sections (gid and name), my favorites first, then by team. By default only projects I am a member of; team narrows to one team."}, s.listProjects)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_users", Annotations: readOnly,
		Description: "List workspace members (gid and name), e.g. to choose an assignee."}, s.listUsers)
	mcp.AddTool(srv, &mcp.Tool{Name: "get_project", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Get a project: its description (Markdown), owner, start and due dates, color, progress (task counts), brief, sections, members and its status updates (Markdown, newest first)."}, s.getProject)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_my_sections", Annotations: readOnly,
		Description: "List the sections of my My Tasks list (e.g. Today / This week / Later), in order. " +
			"Tasks show theirs as my_section; move_task with my_tasks=true moves between them."}, s.listMySections)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_tags", Annotations: readOnly,
		Description: "List tags used by tasks, with the number of open and total tasks for each."}, s.listTags)
	mcp.AddTool(srv, &mcp.Tool{Name: "sync_status", Annotations: readOnly,
		Description: "Show when the cache was last synced and any local changes not yet sent " +
			"(pending, conflicts, failures)."}, s.syncStatus)
	syncDesc := "Send queued local changes to Asana, then pull changes from Asana into the cache " +
		"(incremental; full=true re-fetches everything and removes deleted tasks)."
	if s.ReadOnly {
		syncDesc = "Pull changes from Asana into the cache (incremental; full=true re-fetches everything and removes deleted tasks). " +
			"This server is read-only: queued local changes are not sent (the TUI or `asagao sync` sends them)."
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "sync", Annotations: &mcp.ToolAnnotations{IdempotentHint: true, OpenWorldHint: ptr(true)},
		Description: syncDesc}, s.sync)

	s.addInboxTools(srv)
	s.addGoalTools(srv)
	s.addSearchTools(srv)
	s.addAgentTools(srv)

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
				"assignee, tags, followers, likes, custom fields, dependencies (tasks it waits for / that wait for it), " +
				"its type (milestone / approval), an approval's status, or the projects it is in. Only the fields you pass are changed. " +
				"Use an empty string to clear due_on or assignee."}, s.updateTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "move_task", Annotations: write(false),
			Description: "Move a task to another section of its project (e.g. from \"Ready\" to \"Done\"), " +
				"or to a place in a section: insert_before / insert_after put it just before / after another task " +
				"(without section, in that task's section). project is needed only when the task is in more than one project."}, s.moveTask)
		s.addTaskOpTools(srv, write)
		mcp.AddTool(srv, &mcp.Tool{Name: "rename_section", Annotations: write(false),
			Description: "Rename a section (a list/column) of a project."}, s.renameSection)
		s.addSectionTagTools(srv, write)
		mcp.AddTool(srv, &mcp.Tool{Name: "duplicate_task", Annotations: write(false),
			Description: "Duplicate a task (with its description, assignee, dates, projects, tags, subtasks, attachments and followers)."}, s.duplicateTask)
		mcp.AddTool(srv, &mcp.Tool{Name: "upload_attachment", Annotations: write(true),
			Description: "Attach a local file to a task (max 100MB). The file is sent to Asana, so only upload files the user asked for. " +
				"path must be absolute and inside an allowed folder: " + s.uploadFolders() + ". " +
				"Hidden files and asagao's own files are refused."}, s.uploadAttachment)
		mcp.AddTool(srv, &mcp.Tool{Name: "create_status_update", Annotations: write(false),
			Description: "Post a status update on a project: status (on_track, at_risk, off_track, on_hold, complete), title and a Markdown text."}, s.createStatus)
		mcp.AddTool(srv, &mcp.Tool{Name: "create_project", Annotations: write(false),
			Description: "Create a project in a team (needs Asana to be reachable)."}, s.createProject)
		mcp.AddTool(srv, &mcp.Tool{Name: "update_project", Annotations: write(true),
			Description: "Change a project's description (Markdown), owner, start / due dates or color (queued, sent now if Asana is reachable); " +
				"archive / unarchive it or add / remove its members (needs Asana to be reachable). Ask before archiving."}, s.updateProject)
		mcp.AddTool(srv, &mcp.Tool{Name: "resolve_outbox", Annotations: write(true),
			Description: "Resolve a local change that could not be sent (see sync_status.outbox): keep_local overwrites " +
				"Asana with the local value, keep_remote drops the local change, retry resends a failed entry, discard drops it. " +
				"Ask the user before choosing for a conflict."}, s.resolveOutbox)
		mcp.AddTool(srv, &mcp.Tool{Name: "add_comment", Annotations: write(false),
			Description: "Add a comment to a task."}, s.addComment)
		mcp.AddTool(srv, &mcp.Tool{Name: "update_comment", Annotations: write(true),
			Description: "Rewrite one of my own comments (gid from get_task comments, where mine is true). The new Markdown text replaces the whole comment. " +
				"Comments by other people cannot be changed."}, s.updateComment)
		mcp.AddTool(srv, &mcp.Tool{Name: "delete_comment", Annotations: write(true),
			Description: "Delete one of my own comments (gid from get_task comments, where mine is true). It cannot be undone once sent: " +
				"ask the user before deleting."}, s.deleteComment)
		mcp.AddTool(srv, &mcp.Tool{Name: "delete_attachment", Annotations: write(true),
			Description: "Delete an attachment of a task (gid from get_task attachments). It cannot be undone once sent: " +
				"ask the user before deleting."}, s.deleteAttachment)
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
	Field      string `json:"field,omitempty" jsonschema:"custom field filter: \"name=value\" (option name or value text), or \"name\" for any value"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum number of tasks (default 100, at most 500)"`
	// recent updates (an inbox substitute)
	ModifiedWithinDays int `json:"modified_within_days,omitempty" jsonschema:"only tasks modified in the last N days, newest first (what changed recently)"`
}

type SearchInput struct {
	Query string `json:"query" jsonschema:"words to search for in task names, descriptions and comments"`
	// Remote also searches Asana (Issue #36).
	Remote bool `json:"remote,omitempty" jsonschema:"also search Asana, for tasks the local cache lacks (needs to be online; found tasks are cached)"`
	TaskFilterInput
}

// SearchOutput is TasksOutput with what the search in Asana did (remote only).
type SearchOutput struct {
	Tasks []readmodel.TaskSummary `json:"tasks"`
	Count int                     `json:"count"`
	// RemoteFound is how many tasks Asana found (before the other filters).
	RemoteFound int `json:"remote_found,omitempty"`
	// Note says how Asana was searched, or why it could not be.
	Note string `json:"note,omitempty"`
}

type TasksOutput struct {
	Tasks []readmodel.TaskSummary `json:"tasks"`
	Count int                     `json:"count"`
}

type GetTaskInput struct {
	GID     string `json:"gid" jsonschema:"task gid"`
	Offline bool   `json:"offline,omitempty" jsonschema:"do not contact Asana; use only the local cache"`
}

type GetTaskOutput struct {
	Task *readmodel.TaskDetail `json:"task"`
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
	// Mine is set for my own comments: only these can be changed (update_comment,
	// delete_comment).
	Mine bool `json:"mine,omitempty"`
	// Reactions are the emoji reactions on the comment (reacted: I am one of them).
	Reactions []readmodel.Reaction `json:"reactions,omitempty"`
}

type ListProjectsInput struct {
	All  bool   `json:"all,omitempty" jsonschema:"include projects I am not a member of"`
	Team string `json:"team,omitempty" jsonschema:"only projects of this team (gid or name)"`
}

type ProjectsOutput struct {
	Projects []readmodel.ProjectView `json:"projects"`
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
	// (asagao TUI "!" or `asagao outbox resolve`).
	Outbox []outbox.Op `json:"outbox"`
}

type SyncOutput struct {
	Result sync.Result      `json:"result"`
	Status SyncStatusOutput `json:"status"`
	// Note says why nothing was synced (another asagao process is syncing).
	Note string `json:"note,omitempty"`
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
	GID                string         `json:"gid" jsonschema:"task gid"`
	Completed          *bool          `json:"completed,omitempty" jsonschema:"true to complete, false to reopen"`
	Name               *string        `json:"name,omitempty" jsonschema:"new name"`
	Notes              *string        `json:"notes,omitempty" jsonschema:"new description in Markdown, replacing the old one (keep ![画像](asana:<gid>) lines to keep images)"`
	DueOn              *string        `json:"due_on,omitempty" jsonschema:"new due date (YYYY-MM-DD, today, tomorrow, +7d), or empty string to clear"`
	Assignee           *string        `json:"assignee,omitempty" jsonschema:"\"me\", a user gid or name (see list_users), or empty string to unassign"`
	AddTags            []string       `json:"add_tags,omitempty" jsonschema:"tag names (created if they do not exist) or gids to add"`
	RemoveTags         []string       `json:"remove_tags,omitempty" jsonschema:"tag names or gids to remove"`
	Liked              *bool          `json:"liked,omitempty" jsonschema:"true to like the task, false to unlike"`
	AddFollowers       []string       `json:"add_followers,omitempty" jsonschema:"\"me\", user names or gids to add as followers"`
	RemoveFollowers    []string       `json:"remove_followers,omitempty" jsonschema:"\"me\", user names or gids to remove from followers"`
	AddProjects        []string       `json:"add_projects,omitempty" jsonschema:"project names or gids to also put the task in (first section)"`
	RemoveProjects     []string       `json:"remove_projects,omitempty" jsonschema:"project names or gids to take the task out of"`
	ResourceSubtype    *string        `json:"resource_subtype,omitempty" jsonschema:"task type: default_task, milestone or approval"`
	ApprovalStatus     *string        `json:"approval_status,omitempty" jsonschema:"for an approval task: pending, approved, rejected or changes_requested"`
	AddDependencies    []string       `json:"add_dependencies,omitempty" jsonschema:"gids of tasks this task should wait for (be blocked by)"`
	RemoveDependencies []string       `json:"remove_dependencies,omitempty" jsonschema:"gids of tasks this task should stop waiting for"`
	AddDependents      []string       `json:"add_dependents,omitempty" jsonschema:"gids of tasks that should wait for this task"`
	RemoveDependents   []string       `json:"remove_dependents,omitempty" jsonschema:"gids of tasks that should stop waiting for this task"`
	CustomFields       map[string]any `json:"custom_fields,omitempty" jsonschema:"custom fields to set, by field name or gid (see get_task custom_fields): an option name for enum, a list of option names for multi_enum, a number, text, a date (YYYY-MM-DD, +7d), user names or gids for people; null or empty string clears"`
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
	GID          string `json:"gid" jsonschema:"task gid"`
	Section      string `json:"section,omitempty" jsonschema:"destination section gid or exact name (needed unless insert_before / insert_after is given)"`
	Project      string `json:"project,omitempty" jsonschema:"project gid or name; only needed if the task is in several projects"`
	MyTasks      bool   `json:"my_tasks,omitempty" jsonschema:"move within the sections of My Tasks (e.g. Today / Later; see list_my_sections) instead of a project's; the task must be assigned to me"`
	InsertBefore string `json:"insert_before,omitempty" jsonschema:"gid of a task of the section to put it just before (not with my_tasks)"`
	InsertAfter  string `json:"insert_after,omitempty" jsonschema:"gid of a task of the section to put it just after (not with my_tasks)"`
}

type RenameSectionInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
	Section string `json:"section" jsonschema:"section gid or current exact name"`
	Name    string `json:"name" jsonschema:"new section name"`
}

type TagsOutput struct {
	Tags []readmodel.TagCount `json:"tags"`
}

type AddCommentInput struct {
	GID  string `json:"gid" jsonschema:"task gid"`
	Text string `json:"text" jsonschema:"comment in Markdown (converted to Asana rich text)"`
}

type UpdateCommentInput struct {
	GID  string `json:"gid" jsonschema:"comment gid, from get_task comments; must be my own comment (mine: true)"`
	Text string `json:"text" jsonschema:"the whole new comment in Markdown (converted to Asana rich text)"`
}

type DeleteInput struct {
	GID string `json:"gid" jsonschema:"gid of the comment (from get_task comments; my own only) or of the attachment (from get_task task.attachments)"`
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
	// SectionGID is the section create_section made (its Asana gid once sent).
	SectionGID string `json:"section_gid,omitempty"`
	// Sent is true when the change reached Asana; false means it is queued locally.
	Sent bool   `json:"sent"`
	Note string `json:"note,omitempty"`
	// Discarded lists queued changes dropped because what they change was deleted in Asana.
	Discarded []readmodel.Discard `json:"discarded,omitempty"`
}

// --- read tools ---

// maxLimit is the most rows a read tool returns, whatever limit the client asks for.
const maxLimit = 500

// clampLimit is limit, or def when it is not set, and at most maxLimit.
func clampLimit(limit, def int) int {
	if limit <= 0 {
		return def
	}
	return min(limit, maxLimit)
}

func (s *Server) listTasks(ctx context.Context, _ *mcp.CallToolRequest, in TaskFilterInput) (*mcp.CallToolResult, TasksOutput, error) {
	f, err := s.filter(ctx, in, "me", ptr(false))
	if err != nil {
		return nil, TasksOutput{}, err
	}
	tasks, err := s.query().ListTasks(ctx, f)
	return nil, TasksOutput{Tasks: tasks, Count: len(tasks)}, err
}

func (s *Server) searchTasks(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, SearchOutput, error) {
	if strings.TrimSpace(in.Query) == "" {
		return nil, SearchOutput{}, errors.New("query is required")
	}
	f, err := s.filter(ctx, in.TaskFilterInput, "any", nil)
	if err != nil {
		return nil, SearchOutput{}, err
	}
	f.Query = in.Query
	if !in.Remote {
		tasks, err := s.query().ListTasks(ctx, f)
		return nil, SearchOutput{Tasks: tasks, Count: len(tasks)}, err
	}
	res, err := app.SearchWithAsana(ctx, s.query(), finder{s}, f)
	if err != nil {
		return nil, SearchOutput{}, err
	}
	out := SearchOutput{Tasks: res.Tasks, Count: len(res.Tasks), RemoteFound: res.Found}
	switch {
	case res.RemoteErr != nil:
		out.Note = "Asana could not be searched; these are the local cache's matches only: " + res.RemoteErr.Error()
	case res.ByName:
		out.Note = "The workspace's plan lacks Asana's search API, so Asana matched the start of task names only (not descriptions or comments)."
	}
	return nil, out, nil
}

// finder searches Asana and looks names up there (app.TaskFinder, app.NameFinder) through
// the server's syncer; offline, it fails and the cache is used alone.
type finder struct{ s *Server }

// remoteTimeout bounds one search in Asana, and nameTimeout one name lookup (a write
// waits for it).
const (
	remoteTimeout = 30 * time.Second
	nameTimeout   = 10 * time.Second
)

func (f finder) SearchTasks(ctx context.Context, text string, completed *bool, limit int) (readmodel.RemoteSearchResult, error) {
	syncer, err := f.s.getSyncer(ctx)
	if err != nil {
		return readmodel.RemoteSearchResult{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, remoteTimeout)
	defer cancel()
	return syncer.SearchTasks(ctx, text, completed, limit)
}

func (f finder) FindUsers(ctx context.Context, q string) (int, error) {
	syncer, err := f.s.getSyncer(ctx)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	defer cancel()
	return syncer.FindUsers(ctx, q)
}

func (f finder) FindTags(ctx context.Context, q string) (int, error) {
	syncer, err := f.s.getSyncer(ctx)
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, nameTimeout)
	defer cancel()
	return syncer.FindTags(ctx, q)
}

func (s *Server) filter(ctx context.Context, in TaskFilterInput, defaultAssignee string, defaultCompleted *bool) (readmodel.TaskFilter, error) {
	return s.query().BuildFilter(ctx, readmodel.FilterSpec{
		Assignee: in.Assignee, DefaultAssignee: defaultAssignee,
		Project: in.Project, Section: in.Section, Tag: in.Tag, Field: in.Field,
		DueBefore: in.DueBefore, DueAfter: in.DueAfter,
		Completion: in.Completion, DefaultCompleted: defaultCompleted,
		Overdue: in.Overdue, ModifiedWithinDays: in.ModifiedWithinDays, Limit: clampLimit(in.Limit, 100),
	}, s.Now())
}

func (s *Server) getTask(ctx context.Context, _ *mcp.CallToolRequest, in GetTaskInput) (*mcp.CallToolResult, GetTaskOutput, error) {
	var out GetTaskOutput
	if !in.Offline {
		if err := s.fetchDetails(ctx, in.GID); err != nil {
			out.Note = "Asana could not be reached; showing the local cache: " + err.Error()
		}
	}
	d, err := s.query().GetTask(ctx, in.GID)
	if err != nil {
		return nil, out, err
	}
	out.Task = d
	out.DescriptionMarkdown = editableMarkdown(d.HTMLNotes, d.Notes)
	out.Comments = []CommentOutput{}
	me, _ := s.query().MeGID(ctx) // "" before the first sync: nothing is mine
	for _, st := range d.Stories {
		if st.Type != "comment" {
			continue
		}
		c := CommentOutput{GID: st.GID, CreatedAt: st.CreatedAt, Markdown: editableMarkdown(st.HTMLText, st.Text), Pending: st.Pending,
			Reactions: st.Reactions}
		if st.CreatedBy != nil {
			c.Author = st.CreatedBy.Name
			c.Mine = me != "" && st.CreatedBy.GID == me
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
	md := richtext.FromPlainEditable(readmodel.ReplaceInlineAssets(plain, func(gid string) string {
		return "\n\n" + richtext.ImageToken(gid) + "\n\n"
	}))
	return richtext.ImageTokenRe.ReplaceAllString(md, "![画像](asana:$1)")
}

func (s *Server) listProjects(ctx context.Context, _ *mcp.CallToolRequest, in ListProjectsInput) (*mcp.CallToolResult, ProjectsOutput, error) {
	ps, err := s.query().ListProjectsInTeam(ctx, !in.All, in.Team)
	return nil, ProjectsOutput{Projects: ps}, err
}

type UsersOutput struct {
	Users []readmodel.UserRef `json:"users"`
}

func (s *Server) listUsers(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, UsersOutput, error) {
	users, err := s.query().ListUsers(ctx)
	return nil, UsersOutput{Users: users}, err
}

func (s *Server) listTags(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, TagsOutput, error) {
	tags, err := s.query().ListTags(ctx)
	return nil, TagsOutput{Tags: tags}, err
}

func (s *Server) syncStatus(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, SyncStatusOutput, error) {
	out, err := s.status(ctx)
	return nil, out, err
}

func (s *Server) status(ctx context.Context) (SyncStatusOutput, error) {
	var out SyncStatusOutput
	stats, err := s.query().Stats(ctx)
	if err != nil {
		return out, err
	}
	out.LastSync, out.Tasks, out.Open = stats.LastSync, stats.Tasks, stats.OpenTasks
	if out.Pending, out.Conflict, out.Failed, err = s.query().OutboxCounts(ctx); err != nil {
		return out, err
	}
	out.Outbox, err = s.query().ListOps(ctx)
	return out, err
}

func (s *Server) sync(ctx context.Context, _ *mcp.CallToolRequest, in SyncInput) (*mcp.CallToolResult, SyncOutput, error) {
	var out SyncOutput
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return nil, out, err
	}
	if out.Result, err = syncer.Run(ctx, in.Full); err != nil {
		s.dropSyncerOn(err)
		if help := auth.LoginHelp(err); help != "" {
			return nil, out, fmt.Errorf("sync failed: not logged in to Asana. Tell the user to log in again (they must run it in a terminal): %s", help)
		}
		if errors.Is(err, sync.ErrAccountChanged) {
			return nil, out, fmt.Errorf("sync stopped: %s", s.accountChangedNote(ctx))
		}
		if !errors.Is(err, sync.ErrPushBusy) {
			return nil, out, err
		}
		out.Note = "another asagao process (the TUI, the CLI or another MCP server) is syncing right now, so nothing was done; " +
			"queued changes are sent by that process. Try again in a moment if you need fresh data."
	}
	out.Status, err = s.status(ctx)
	return nil, out, err
}

// --- write tools ---

func (s *Server) createTask(ctx context.Context, _ *mcp.CallToolRequest, in CreateTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	tasks := app.Tasks{Store: s.Store, Text: richtext.Converter{}, Clock: s.Now, Names: finder{s}}
	gid, err := tasks.Create(ctx, app.CreateTaskInput{
		Name: in.Name, Parent: in.Parent, Project: in.Project, Section: in.Section,
		Due: in.DueOn, Assignee: in.Assignee, Notes: in.Notes, Tags: in.Tags,
	})
	var fe *app.FieldError
	if errors.As(err, &fe) {
		if fe.Field == "due" {
			return nil, WriteOutput{}, fmt.Errorf("due_on: %w", fe.Err)
		}
		return nil, WriteOutput{}, fe.Err // the assignee's error is reported as the store gives it
	}
	if err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, gid), nil
}

// updateTask resolves and checks all of its input before writing anything, so that a bad
// name (tag, follower, project) does not leave half of the update saved.
func (s *Server) updateTask(ctx context.Context, _ *mcp.CallToolRequest, in UpdateTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	now := s.Now()
	tasks := app.Tasks{Store: s.Store, Text: richtext.Converter{}, Clock: func() time.Time { return now }, Names: finder{s}}
	changes, err := tasks.ResolveUpdate(ctx, app.UpdateTaskInput{
		Name: in.Name, Notes: in.Notes, Due: in.DueOn, Assignee: in.Assignee,
		Subtype: in.ResourceSubtype, Approval: in.ApprovalStatus, Completed: in.Completed, Liked: in.Liked,
	})
	if fe := (*app.FieldError)(nil); errors.As(err, &fe) {
		switch fe.Field {
		case "due":
			return nil, WriteOutput{}, fmt.Errorf("due_on: %w", fe.Err)
		case "assignee":
			return nil, WriteOutput{}, fe.Err // as the store says it
		default: // resource_subtype, approval_status: the argument names
			return nil, WriteOutput{}, fmt.Errorf("%s: %w", fe.Field, fe.Err)
		}
	}
	if err != nil {
		return nil, WriteOutput{}, err
	}
	relations := app.Relations{Store: s.Store, Clock: func() time.Time { return now }}
	edit, err := relations.Resolve(ctx, in.GID, app.RelationsInput{
		AddTags: in.AddTags, RemoveTags: in.RemoveTags,
		AddFollowers: in.AddFollowers, RemoveFollowers: in.RemoveFollowers,
		AddProjects: in.AddProjects, RemoveProjects: in.RemoveProjects,
		AddDependencies: in.AddDependencies, RemoveDependencies: in.RemoveDependencies,
		AddDependents: in.AddDependents, RemoveDependents: in.RemoveDependents,
	})
	if err != nil {
		return nil, WriteOutput{}, err // a *app.FieldError reads "add_followers: …", the argument's name
	}
	if changes.Count() == 0 && edit.Empty() && len(in.CustomFields) == 0 {
		return nil, WriteOutput{}, errors.New("nothing to update: pass at least one field to change")
	}
	if _, err := s.query().GetTask(ctx, in.GID); err != nil {
		return nil, WriteOutput{}, err
	}
	// Resolve every custom field value before changing anything.
	fields, err := relations.ResolveFields(ctx, in.GID, in.CustomFields)
	if err != nil {
		return nil, WriteOutput{}, err // "custom_fields: …"
	}

	if err := tasks.UpdateHere(ctx, in.GID, changes); err != nil {
		return nil, WriteOutput{}, err
	}
	if err := relations.Apply(ctx, in.GID, edit, fields); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
}

func (s *Server) moveTask(ctx context.Context, _ *mcp.CallToolRequest, in MoveTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	sections := app.Sections{Store: s.Store, Clock: s.Now}
	if in.MyTasks {
		if in.Project != "" {
			return nil, WriteOutput{}, fmt.Errorf("my_tasks and project cannot be used together")
		}
		if in.InsertBefore != "" || in.InsertAfter != "" {
			return nil, WriteOutput{}, fmt.Errorf("insert_before / insert_after cannot be used with my_tasks")
		}
		if err := sections.MoveMine(ctx, in.GID, in.Section); err != nil {
			return nil, WriteOutput{}, err
		}
		return nil, s.push(ctx, in.GID), nil
	}
	if err := sections.MoveAt(ctx, in.GID, in.Project, in.Section, model.Position{Before: in.InsertBefore, After: in.InsertAfter}); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
}

func (s *Server) renameSection(ctx context.Context, _ *mcp.CallToolRequest, in RenameSectionInput) (*mcp.CallToolResult, WriteOutput, error) {
	sections := app.Sections{Store: s.Store, Clock: s.Now}
	if err := sections.Rename(ctx, in.Project, in.Section, in.Name); err != nil {
		return nil, WriteOutput{}, err
	}
	out := s.push(ctx, "")
	return nil, out, nil
}

func (s *Server) addComment(ctx context.Context, _ *mcp.CallToolRequest, in AddCommentInput) (*mcp.CallToolResult, WriteOutput, error) {
	comments := app.Comments{Store: s.Store, Text: richtext.Converter{}, Clock: s.Now}
	if _, err := comments.Add(ctx, in.GID, in.Text); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
}

func (s *Server) updateComment(ctx context.Context, _ *mcp.CallToolRequest, in UpdateCommentInput) (*mcp.CallToolResult, WriteOutput, error) {
	comments := app.Comments{Store: s.Store, Text: richtext.Converter{}, Clock: s.Now}
	if err := comments.Edit(ctx, in.GID, in.Text); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

func (s *Server) deleteComment(ctx context.Context, _ *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, WriteOutput, error) {
	comments := app.Comments{Store: s.Store, Text: richtext.Converter{}, Clock: s.Now}
	if err := comments.Delete(ctx, in.GID); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

func (s *Server) deleteAttachment(ctx context.Context, _ *mcp.CallToolRequest, in DeleteInput) (*mcp.CallToolResult, WriteOutput, error) {
	copies := app.Copies{Store: s.Store, Clock: s.Now}
	if err := copies.Detach(ctx, in.GID); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

// accountChangedNote explains sync.ErrAccountChanged to the model. Resetting is left to
// the user (TUI or CLI): this server never deletes local data.
func (s *Server) accountChangedNote(ctx context.Context) string {
	n, _ := s.query().UnsentCount(ctx)
	return fmt.Sprintf("the Asana account now logged in is not the one this local data was synced with, so syncing is stopped "+
		"(%d unsent change(s) from the previous account). Tell the user, and let them choose: log in again with the previous "+
		"account (`asagao auth login`) to send the changes, or reset the local data to use the new account "+
		"(`asagao reset`, or \"ローカルデータを初期化\" in the TUI menu; unsent changes are backed up to a JSON file first). "+
		"This MCP server cannot reset. (%s)", n, readmodel.AccountChangedHelp(n, "asagao reset"))
}

// push sends the outbox now. A failure is reported in the output, not as a tool error:
// the change is saved and will be sent by the next sync.
func (s *Server) push(ctx context.Context, taskGID string) WriteOutput {
	out := WriteOutput{TaskGID: taskGID}
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		out.Note = "saved locally; will be sent on the next sync (" + err.Error() + ")"
		if help := auth.LoginHelp(err); help != "" {
			out.Note = "saved locally but NOT sent: not logged in to Asana. Tell the user to log in (in a terminal); " +
				"the change is sent by the next sync after that: " + help
		}
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, err := syncer.Push(ctx)
	s.dropSyncerOn(err)
	out.Discarded = res.Discarded
	if len(res.Discarded) > 0 {
		out.Note = fmt.Sprintf("%d change(s) to items deleted in Asana were discarded; ", len(res.Discarded))
	}
	switch {
	case errors.Is(err, sync.ErrAccountChanged):
		out.Note += "saved locally but NOT sent: " + s.accountChangedNote(ctx)
	case auth.LoginHelp(err) != "":
		out.Note += "saved locally but NOT sent: Asana rejected the login token. Tell the user to log in again (in a terminal); " +
			"the change is sent by the next sync after that: " + auth.LoginHelp(err)
	case errors.Is(err, sync.ErrPushBusy):
		out.Note += "saved locally; another asagao process is sending the outbox right now and will send this change too"
	case err != nil:
		out.Note += "saved locally; Asana could not be reached, will be sent on the next sync (" + err.Error() + ")"
	case res.InboxReadOnly:
		out.Note += "Asana refused to mark notifications read / unread, so the change was undone: " + sync.InboxReadOnlyMessage(s.AuthScopes)
	case res.Conflicts > 0 || res.Failed > 0:
		out.Note += fmt.Sprintf("%d conflict(s) / %d failure(s) in the outbox; see sync_status", res.Conflicts, res.Failed)
	default:
		out.Sent = true
	}
	if real, err := s.query().RealGID(ctx, taskGID); err == nil {
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
	// Read-only: only pull. The outbox may hold changes the user queued in the TUI or
	// CLI; an agent must not be able to send them.
	syncer.PullOnly = s.ReadOnly
	s.syncer = syncer
	return syncer, nil
}

// dropSyncerOn forgets the syncer after a login problem or another account, so the next
// call builds it again and picks up a login made meanwhile (asagao auth login).
func (s *Server) dropSyncerOn(err error) {
	if auth.LoginHelp(err) == "" && !errors.Is(err, sync.ErrAccountChanged) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncer = nil
}

func ptr[T any](v T) *T { return &v }

func (s *Server) resolveOutbox(ctx context.Context, _ *mcp.CallToolRequest, in ResolveOutboxInput) (*mcp.CallToolResult, WriteOutput, error) {
	action, err := app.ParseResolveAction(in.Action)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	r, err := (&app.Outbox{Store: s.Store}).Resolve(ctx, in.ID, action)
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
	out := AttachmentOutput{LocalPath: path}
	if a, err := s.query().GetAttachment(ctx, in.GID); err == nil {
		out.Name, out.IsImage = a.Name, a.IsImage
	} else if errors.Is(err, readmodel.ErrNotFound) {
		// not recorded: its task in Asana is not in the cache (e.g. an image copied from
		// another task); the cached file is named "<gid>-name"
		out.Name = strings.TrimPrefix(filepath.Base(path), in.GID+"-")
		out.IsImage = readmodel.IsImageName(out.Name)
	} else {
		return nil, AttachmentOutput{}, err
	}
	if !out.IsImage {
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
		&mcp.TextContent{Text: out.Name + " (" + path + ")"},
	}}
	return res, out, nil
}

func (s *Server) duplicateTask(ctx context.Context, _ *mcp.CallToolRequest, in DuplicateTaskInput) (*mcp.CallToolResult, WriteOutput, error) {
	copies := app.Copies{Store: s.Store, CacheDir: s.CacheDir, Clock: s.Now}
	gid, err := copies.Duplicate(ctx, in.GID, in.Name)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, gid), nil
}

func (s *Server) uploadAttachment(ctx context.Context, _ *mcp.CallToolRequest, in UploadAttachmentInput) (*mcp.CallToolResult, WriteOutput, error) {
	path, err := s.Upload.Check(in.Path)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	copies := app.Copies{Store: s.Store, CacheDir: s.CacheDir, Clock: s.Now}
	if _, err := copies.Attach(ctx, in.TaskGID, path); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.TaskGID), nil
}

type ProjectInput struct {
	Project string `json:"project" jsonschema:"project gid or name"`
}

type ProjectOutput struct {
	Project readmodel.ProjectView `json:"project"`
	// The overview: the description as Markdown (what update_project accepts back), the
	// owner, dates, color, progress and brief.
	Markdown string                `json:"markdown"`
	Owner    *readmodel.UserRef    `json:"owner,omitempty"`
	StartOn  string                `json:"start_on,omitempty"`
	DueOn    string                `json:"due_on,omitempty"`
	Color    string                `json:"color,omitempty"`
	Counts   *readmodel.TaskCounts `json:"task_counts,omitempty"`
	Brief    *BriefOutput          `json:"brief,omitempty"`
	// Pending: changes to the fields above are queued and not sent yet.
	Pending  bool                `json:"pending,omitempty"`
	Members  []readmodel.UserRef `json:"members"`
	Statuses []StatusOutput      `json:"status_updates"`
	Note     string              `json:"note,omitempty"`
}

// BriefOutput is a project's brief (read only).
type BriefOutput struct {
	Title    string `json:"title,omitempty"`
	Markdown string `json:"markdown"`
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
	gid, err := s.query().ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, out, err
	}
	if syncer, err := s.getSyncer(ctx); err == nil {
		if err := syncer.FetchStatusUpdates(ctx, gid); err != nil {
			out.Note = "status updates from the local cache: " + err.Error()
		}
		if err := syncer.FetchProjectOverview(ctx, gid); err != nil {
			out.Note = strings.TrimPrefix(out.Note+"; ", "; ") + "overview from the local cache: " + err.Error()
		}
	}
	o, err := s.query().ProjectOverview(ctx, gid)
	if err != nil {
		return nil, out, err
	}
	out.Markdown = editableMarkdown(o.HTMLNotes, o.Notes)
	out.Owner, out.StartOn, out.DueOn, out.Color, out.Counts, out.Pending = o.Owner, o.StartOn, o.DueOn, o.Color, o.Counts, o.Pending
	if o.Brief != nil {
		out.Brief = &BriefOutput{Title: o.Brief.Title, Markdown: editableMarkdown(o.Brief.HTMLText, o.Brief.Text)}
	}
	projects, err := s.query().ListProjects(ctx, false)
	if err != nil {
		return nil, out, err
	}
	for _, p := range projects {
		if p.GID == gid {
			out.Project = p
		}
	}
	if out.Members, err = s.query().ProjectMembers(ctx, gid); err != nil {
		return nil, out, err
	}
	updates, err := s.query().StatusUpdates(ctx, gid)
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
	statuses := app.Status{Store: s.Store, Text: richtext.Converter{}, Clock: s.Now}
	if _, err := statuses.Create(ctx, app.StatusInput{Project: in.Project, Status: in.Status, Title: in.Title, Text: in.Text}); err != nil {
		return nil, WriteOutput{}, err
	}
	out := s.push(ctx, "")
	return nil, out, nil
}

type CreateProjectInput struct {
	Name string `json:"name" jsonschema:"project name"`
	Team string `json:"team,omitempty" jsonschema:"team gid or name (needed when I am in more than one team)"`
}

type ProjectWriteOutput struct {
	ProjectGID string `json:"project_gid"`
	// Sent is set when fields were changed: true when the change reached Asana, false when
	// it is queued locally.
	Sent *bool  `json:"sent,omitempty"`
	Note string `json:"note,omitempty"`
}

func (s *Server) createProject(ctx context.Context, _ *mcp.CallToolRequest, in CreateProjectInput) (*mcp.CallToolResult, ProjectWriteOutput, error) {
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("Asana に接続できないため作れません: %w", err)
	}
	team, err := s.query().TeamForNewProject(ctx, in.Team)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("team: %w", err)
	}
	p, err := syncer.CreateProject(ctx, in.Name, team)
	if err != nil {
		return nil, ProjectWriteOutput{}, err
	}
	return nil, ProjectWriteOutput{ProjectGID: p.GID}, nil
}

type UpdateProjectInput struct {
	Project       string   `json:"project" jsonschema:"project gid or name"`
	Notes         *string  `json:"notes,omitempty" jsonschema:"new description in Markdown (replaces it)"`
	Owner         *string  `json:"owner,omitempty" jsonschema:"\"me\", a user name or gid; \"\" removes the owner"`
	StartOn       *string  `json:"start_on,omitempty" jsonschema:"start date YYYY-MM-DD (needs a due date), \"\" to clear"`
	DueOn         *string  `json:"due_on,omitempty" jsonschema:"due date YYYY-MM-DD, \"\" to clear"`
	Color         *string  `json:"color,omitempty" jsonschema:"dark-pink, dark-green, dark-blue, dark-red, dark-teal, dark-brown, dark-orange, dark-purple, dark-warm-gray, light-... (the same colors), or none"`
	Archived      *bool    `json:"archived,omitempty" jsonschema:"true to archive, false to unarchive"`
	AddMembers    []string `json:"add_members,omitempty" jsonschema:"\"me\", user names or gids to add"`
	RemoveMembers []string `json:"remove_members,omitempty" jsonschema:"\"me\", user names or gids to remove"`
}

// updateProject checks all of its input before changing anything in Asana.
func (s *Server) updateProject(ctx context.Context, _ *mcp.CallToolRequest, in UpdateProjectInput) (*mcp.CallToolResult, ProjectWriteOutput, error) {
	gid, err := s.query().ResolveProject(ctx, in.Project)
	if err != nil {
		return nil, ProjectWriteOutput{}, err
	}
	add, err := s.query().ResolveUsers(ctx, in.AddMembers)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("add_members: %w", err)
	}
	remove, err := s.query().ResolveUsers(ctx, in.RemoveMembers)
	if err != nil {
		return nil, ProjectWriteOutput{}, fmt.Errorf("remove_members: %w", err)
	}
	projects := app.Projects{Store: s.Store, Text: richtext.Converter{}, Clock: s.Now}
	fields, err := projects.ResolveUpdate(ctx, app.UpdateProjectInput{Notes: in.Notes, Owner: in.Owner, StartOn: in.StartOn, DueOn: in.DueOn, Color: in.Color})
	if err != nil {
		return nil, ProjectWriteOutput{}, err
	}
	members := len(add)+len(remove) > 0
	if !members && in.Archived == nil && fields.Count() == 0 {
		return nil, ProjectWriteOutput{}, errors.New("nothing to update: pass notes, owner, start_on, due_on, color, archived, add_members or remove_members")
	}
	if fields.Count() > 0 {
		// Queued first (it works offline), then sent with the other changes.
		if err := projects.UpdateHere(ctx, gid, fields); err != nil {
			return nil, ProjectWriteOutput{}, err
		}
		if !members && in.Archived == nil {
			w := s.push(ctx, "")
			return nil, ProjectWriteOutput{ProjectGID: gid, Sent: &w.Sent, Note: w.Note}, nil
		}
	}
	syncer, err := s.getSyncer(ctx)
	if err != nil {
		if fields.Count() > 0 {
			return nil, ProjectWriteOutput{}, fmt.Errorf("the fields were saved locally and will be sent on the next sync, "+
				"but archiving / members need Asana, which cannot be reached: %w", err)
		}
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
	if fields.Count() > 0 {
		steps = append([]step{{"fields (queued)", func() error { return nil }}}, steps...)
	}
	for i, st := range steps {
		if err := st.run(); err != nil {
			if i > 0 {
				return nil, ProjectWriteOutput{}, fmt.Errorf("%s was applied, but %s failed: %w", steps[0].name, st.name, err)
			}
			return nil, ProjectWriteOutput{}, err
		}
	}
	out := ProjectWriteOutput{ProjectGID: gid}
	if fields.Count() > 0 {
		w := s.push(ctx, "")
		out.Sent, out.Note = &w.Sent, w.Note
	}
	return nil, out, nil
}
