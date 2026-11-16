package app_test

import (
	"github.com/amasok/asana-tui/internal/app"
	"github.com/amasok/asana-tui/internal/richtext"
	"github.com/amasok/asana-tui/internal/store"
)

// The adapters satisfy the ports as they are.
var (
	_ app.TaskStore    = (*store.Store)(nil)
	_ app.CommentStore = (*store.Store)(nil)
	_ app.StatusStore  = (*store.Store)(nil)
	_ app.RichText     = richtext.Converter{}
)
