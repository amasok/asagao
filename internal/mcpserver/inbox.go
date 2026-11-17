package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// The inbox tools: list_inbox (read) and mark_notifications (write, hidden with ReadOnly).
func (s *Server) addInboxTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{Name: "list_inbox", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		Description: "List my Asana inbox notifications (comments, assignments, mentions... on tasks) from the local cache, " +
			"newest first, with read state and the task each one is about (open it with get_task). " +
			"If notifications cannot be fetched (e.g. the login lacks the notifications:read scope), available is false, " +
			"notice says why and what the user can do, and recent lists the tasks modified in the last 7 days instead."}, s.listInbox)
	if s.ReadOnly {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "mark_notifications", Annotations: &mcp.ToolAnnotations{IdempotentHint: true, DestructiveHint: ptr(false), OpenWorldHint: ptr(true)},
		Description: "Mark inbox notifications read (read=true, the default) or unread. Pass notification gids from list_inbox, " +
			"or all=true for every notification in the cache. Queued and sent like other writes."}, s.markNotifications)
}

type ListInboxInput struct {
	Unread bool `json:"unread,omitempty" jsonschema:"only unread notifications"`
	Limit  int  `json:"limit,omitempty" jsonschema:"maximum number of notifications (default 50, at most 500)"`
}

type InboxOutput struct {
	Available     bool                 `json:"available"`
	Notice        string               `json:"notice,omitempty"`
	Unread        int                  `json:"unread"`
	Notifications []store.Notification `json:"notifications"`
	Recent        []store.TaskSummary  `json:"recent,omitempty"`
}

type MarkNotificationsInput struct {
	GIDs []string `json:"gids,omitempty" jsonschema:"notification gids (from list_inbox)"`
	All  bool     `json:"all,omitempty" jsonschema:"every cached notification"`
	Read *bool    `json:"read,omitempty" jsonschema:"true = mark read (default), false = mark unread"`
}

type MarkNotificationsOutput struct {
	WriteOutput
	Changed int `json:"changed"`
}

func (s *Server) listInbox(ctx context.Context, _ *mcp.CallToolRequest, in ListInboxInput) (*mcp.CallToolResult, InboxOutput, error) {
	limit := clampLimit(in.Limit, 50)
	state, err := s.Store.InboxState(ctx)
	if err != nil {
		return nil, InboxOutput{}, err
	}
	out := InboxOutput{Available: state.Available(), Notifications: []store.Notification{}}
	if out.Available && state.WriteForbidden {
		out.Notice = sync.InboxReadOnlyMessage(s.AuthScopes)
	}
	if !out.Available {
		out.Notice = sync.InboxUnavailableMessage(state, s.AuthScopes)
		out.Recent, err = s.Store.ListTasks(ctx, store.TaskFilter{
			ModifiedAfter:  s.Now().AddDate(0, 0, -7).UTC().Format(time.RFC3339),
			SortByModified: true, Limit: limit,
		})
		return nil, out, err
	}
	if out.Notifications, err = s.Store.ListNotifications(ctx, store.NotificationFilter{UnreadOnly: in.Unread, Limit: limit}); err != nil {
		return nil, out, err
	}
	out.Unread, err = s.Store.UnreadNotifications(ctx)
	return nil, out, err
}

func (s *Server) markNotifications(ctx context.Context, _ *mcp.CallToolRequest, in MarkNotificationsInput) (*mcp.CallToolResult, MarkNotificationsOutput, error) {
	read := in.Read == nil || *in.Read
	gids := in.GIDs
	if in.All {
		ns, err := s.Store.ListNotifications(ctx, store.NotificationFilter{})
		if err != nil {
			return nil, MarkNotificationsOutput{}, err
		}
		gids = gids[:0:0]
		for _, n := range ns {
			gids = append(gids, n.GID)
		}
	} else if len(gids) == 0 {
		return nil, MarkNotificationsOutput{}, errors.New("pass gids or all=true")
	}
	notifications := app.Notifications{Store: s.Store, Clock: s.Now}
	n, err := notifications.Mark(ctx, gids, read)
	if err != nil {
		return nil, MarkNotificationsOutput{}, err
	}
	if n == 0 {
		return nil, MarkNotificationsOutput{WriteOutput: WriteOutput{Sent: true, Note: "nothing to change"}}, nil
	}
	return nil, MarkNotificationsOutput{WriteOutput: s.push(ctx, ""), Changed: n}, nil
}
