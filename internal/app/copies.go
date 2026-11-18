package app

import (
	"context"
	"time"
)

// CopyStore is the part of the local cache that duplicating a task and attaching a file
// need. *store.Store satisfies it.
type CopyStore interface {
	DuplicateTaskLocal(ctx context.Context, gid, name string, now time.Time) (string, error)
	UploadAttachmentLocal(ctx context.Context, taskGID, srcPath, cacheDir string, now time.Time) (string, error)
	DeleteAttachmentLocal(ctx context.Context, gid string, now time.Time) error
}

// Copies is the use cases that copy something into Asana: a duplicate of a task, and a
// file attached to a task. Each is one local write, queued in the outbox.
type Copies struct {
	Store CopyStore
	// CacheDir is where an attached file is copied until it is sent.
	CacheDir string
	Clock    Clock
}

// Duplicate duplicates task gid, named name ("" for "… のコピー"), with its subtasks,
// attachments and followers once it is sent. It returns the copy's local gid. The errors
// are the store's, as they are.
func (c *Copies) Duplicate(ctx context.Context, gid, name string) (string, error) {
	return c.Store.DuplicateTaskLocal(ctx, gid, name, c.Clock())
}

// Attach attaches the file at path to task taskGID: the file is copied into CacheDir and
// sent on the next push. Which paths an entry point accepts is its own policy (the MCP
// server's UploadPolicy), checked before. It returns the attachment's local gid. The
// errors are the store's, as they are (a missing file, one over the size limit).
func (c *Copies) Attach(ctx context.Context, taskGID, path string) (string, error) {
	return c.Store.UploadAttachmentLocal(ctx, taskGID, path, c.CacheDir, c.Clock())
}

// Detach deletes the attachment gid, locally and queued in the outbox (a file not uploaded
// yet is just dropped). The errors are the store's, as they are.
func (c *Copies) Detach(ctx context.Context, gid string) error {
	return c.Store.DeleteAttachmentLocal(ctx, gid, c.Clock())
}
