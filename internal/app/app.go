// Package app holds the use cases that the entry points (TUI, CLI, MCP) share: it reads
// the input, resolves names, converts rich text and calls the store's local writes, which
// apply the change and queue it in the outbox. Sending is left to the entry point.
//
// app depends only on internal/domain/... (and internal/dates) and the standard library.
// What it needs from the outside comes through the ports below, which the adapters satisfy
// as they are (*store.Store, richtext.Converter). See docs/design/adr/0006-application-use-cases.md.
package app

import (
	"context"
	"time"

	"github.com/amasok/asana-tui/internal/domain/task"
	"github.com/amasok/asana-tui/internal/readmodel"
)

// TaskStore is the part of the local cache that task use cases need. *store.Store
// satisfies it.
type TaskStore interface {
	ResolveProject(ctx context.Context, arg string) (string, error)
	ResolveSection(ctx context.Context, projectGID, arg string) (string, error)
	FirstSection(ctx context.Context, projectGID string) (string, error)
	DefaultWorkspace(ctx context.Context) (string, error)
	ResolveUser(ctx context.Context, arg string) (string, error)
	ResolveTagEdit(ctx context.Context, add, remove []string) (task.TagEdit, error)
	CreateTaskLocal(ctx context.Context, nt task.NewTask, now time.Time) (string, error)
	ApplyTagEdit(ctx context.Context, taskGID string, e task.TagEdit, now time.Time) error
}

// CommentStore is the part of the local cache that the comment use case needs.
// *store.Store satisfies it.
type CommentStore interface {
	Me(ctx context.Context) (readmodel.UserRef, error)
	AddRichCommentLocal(ctx context.Context, taskGID, text, htmlText string, author readmodel.UserRef, now time.Time) (string, error)
}

// StatusStore is the part of the local cache that the status update use cases need.
// *store.Store satisfies it.
type StatusStore interface {
	ResolveProject(ctx context.Context, arg string) (string, error)
	Me(ctx context.Context) (readmodel.UserRef, error)
	CreateStatusLocal(ctx context.Context, projectGID, statusType, title, text, htmlText string, author readmodel.UserRef, now time.Time) (string, error)
}

// RichText converts the Markdown a user writes to Asana rich text, with one method per
// field it is sent to (they accept different tags). richtext.Converter satisfies it.
type RichText interface {
	// TaskNotes converts a task description to html_notes and its plain notes.
	TaskNotes(md string) (html, plain string)
	// Comment converts a comment to a story's html_text and its plain text.
	Comment(md string) (html, plain string)
	// StatusUpdate converts the body of a status update to its html_text and plain text.
	StatusUpdate(md string) (html, plain string)
}

// Clock returns the current time. Use cases take it instead of calling time.Now.
type Clock func() time.Time

// FieldError is an invalid input value. Field names the input ("due", "assignee"), so
// that each entry point can report it in its own words (a CLI flag, an MCP argument).
type FieldError struct {
	Field string
	Err   error
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Err.Error() }
func (e *FieldError) Unwrap() error { return e.Err }
