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

// Edit replaces the text of one of the user's comments (storyGID) with md, written in
// Markdown, locally and queued in the outbox. A comment not sent yet is sent with the new
// text instead. The errors are the store's, as they are (someone else's comment, an empty
// comment, a comment not in the cache).
func (c *Comments) Edit(ctx context.Context, storyGID, md string) error {
	html, plain := c.Text.Comment(md)
	return c.Store.EditCommentLocal(ctx, storyGID, plain, html, c.Clock())
}

// Delete deletes one of the user's comments, locally and queued in the outbox (a comment
// not sent yet is just dropped). The errors are the store's, as they are.
func (c *Comments) Delete(ctx context.Context, storyGID string) error {
	return c.Store.DeleteCommentLocal(ctx, storyGID, c.Clock())
}
