package mcpserver

import (
	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/sync"
)

// Store is the local cache as the MCP tools use it: the ports of the use cases and of the
// sync engine. Store satisfies it; the server does not import the store.
type Store interface {
	app.TaskStore
	app.CommentStore
	app.StatusStore
	app.ProjectStore
	app.RelationStore
	app.SectionStore
	app.TaskOpsStore
	app.TagStore
	app.CopyStore
	app.NotificationStore
	app.OutboxStore
	app.QueryStore
	sync.LocalStore
}
