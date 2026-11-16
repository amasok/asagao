package app

import "context"

// Comments is the comment use case.
type Comments struct {
	Store CommentStore
	Text  RichText
	Clock Clock
}

// Add adds a comment, written in Markdown, to the task taskGID (a gid, not a name) as me,
// locally and queued in the outbox. It returns the comment's local gid. The errors are the
// store's, as they are (no logged-in user, an empty comment, a task not in the cache).
func (c *Comments) Add(ctx context.Context, taskGID, md string) (string, error) {
	me, err := c.Store.Me(ctx)
	if err != nil {
		return "", err
	}
	html, plain := c.Text.Comment(md)
	return c.Store.AddRichCommentLocal(ctx, taskGID, plain, html, me, c.Clock())
}
