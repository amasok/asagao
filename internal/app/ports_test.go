package app_test

import (
	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/store"
)

// The adapters satisfy the ports as they are.
var (
	_ app.TaskStore     = (*store.Store)(nil)
	_ app.CommentStore  = (*store.Store)(nil)
	_ app.StatusStore   = (*store.Store)(nil)
	_ app.RelationStore = (*store.Store)(nil)
	_ app.SectionStore  = (*store.Store)(nil)
	_ app.RichText      = richtext.Converter{}
)
