package mcpserver

import (
	"context"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/readmodel"
)

// Tools for the time tracking entries of tasks (#6). Asana offers them on paid plans only:
// once it refused them with a 402 the tools are not offered.

type TimeEntriesInput struct {
	Task string `json:"task" jsonschema:"task gid"`
}

type TimeEntriesOutput struct {
	readmodel.TimeEntries
	Note string `json:"note,omitempty"`
}

type AddTimeEntryInput struct {
	Task     string `json:"task" jsonschema:"task gid"`
	Duration string `json:"duration" jsonschema:"time worked: minutes (90) or 1h30m / 1.5h / 1:30; at most 24h"`
	Date     string `json:"date,omitempty" jsonschema:"day it was worked: YYYY-MM-DD, today, yesterday or -Nd (default today)"`
}

type UpdateTimeEntryInput struct {
	GID      string `json:"gid" jsonschema:"time entry gid (from list_time_entries)"`
	Duration string `json:"duration,omitempty" jsonschema:"new time worked (omit to keep it)"`
	Date     string `json:"date,omitempty" jsonschema:"new day (omit to keep it)"`
}

type DeleteTimeEntryInput struct {
	GID string `json:"gid" jsonschema:"time entry gid (from list_time_entries)"`
}

func (s *Server) timeTracking() *app.TimeTracking {
	return &app.TimeTracking{Store: s.Store, Clock: s.Now}
}

func (s *Server) addTimeTrackingTools(srv *mcp.Server) {
	if !s.timeTracking().Available(context.Background()) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "list_time_entries", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)},
		Description: "List the time worked on a task (Asana time tracking entries: day, duration, who) and the total. " +
			"Read from Asana when it is reachable, else from the local cache."}, s.listTimeEntries)
}

func (s *Server) addTimeTrackingWriteTools(srv *mcp.Server, write func(destructive bool) *mcp.ToolAnnotations) {
	if !s.timeTracking().Available(context.Background()) {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "add_time_entry", Annotations: write(false),
		Description: "Record time worked on a task (an Asana time tracking entry) as me, for a day (default today)."}, s.addTimeEntry)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_time_entry", Annotations: write(true),
		Description: "Change the duration and/or the day of a time tracking entry (gid from list_time_entries)."}, s.updateTimeEntry)
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_time_entry", Annotations: write(true),
		Description: "Delete a time tracking entry (gid from list_time_entries). It cannot be undone once sent: " +
			"ask the user before deleting."}, s.deleteTimeEntry)
}

func (s *Server) listTimeEntries(ctx context.Context, _ *mcp.CallToolRequest, in TimeEntriesInput) (*mcp.CallToolResult, TimeEntriesOutput, error) {
	var out TimeEntriesOutput
	if syncer, err := s.getSyncer(ctx); err != nil {
		out.Note = "from the local cache (Asana not reachable: " + err.Error() + ")"
	} else {
		fctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := syncer.FetchTimeEntries(fctx, in.Task)
		cancel()
		if app.IsPlanUnavailable(err) {
			return nil, out, err
		}
		if err != nil {
			out.Note = "from the local cache: " + err.Error()
		}
	}
	es, err := s.timeTracking().List(ctx, in.Task)
	if err != nil {
		return nil, out, err
	}
	out.TimeEntries = es
	return nil, out, nil
}

func (s *Server) addTimeEntry(ctx context.Context, _ *mcp.CallToolRequest, in AddTimeEntryInput) (*mcp.CallToolResult, WriteOutput, error) {
	g, err := s.timeTracking().Add(ctx, in.Task, in.Duration, in.Date)
	if err != nil {
		return nil, WriteOutput{}, err
	}
	out := s.push(ctx, in.Task)
	out.TimeEntryGID = g
	if real, waiting, err := s.Store.LocalResult(ctx, readmodel.ResultTimeEntry, g); err == nil && real != "" && !waiting {
		out.TimeEntryGID = real
	}
	return nil, out, nil
}

func (s *Server) updateTimeEntry(ctx context.Context, _ *mcp.CallToolRequest, in UpdateTimeEntryInput) (*mcp.CallToolResult, WriteOutput, error) {
	if in.Duration == "" && in.Date == "" {
		return nil, WriteOutput{}, errors.New("duration か date を指定してください")
	}
	if err := s.timeTracking().Edit(ctx, in.GID, in.Duration, in.Date); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}

func (s *Server) deleteTimeEntry(ctx context.Context, _ *mcp.CallToolRequest, in DeleteTimeEntryInput) (*mcp.CallToolResult, WriteOutput, error) {
	if err := s.timeTracking().Delete(ctx, in.GID); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, ""), nil
}
