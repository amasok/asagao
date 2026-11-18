package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
)

// addTaskOpTools registers the tools that reorganize tasks (#32): deleting a task and
// changing its parent. Moving to a place in a section is move_task's insert_before /
// insert_after.
func (s *Server) addTaskOpTools(srv *mcp.Server, write func(destructive bool) *mcp.ToolAnnotations) {
	mcp.AddTool(srv, &mcp.Tool{Name: "delete_task", Annotations: write(true),
		Description: "Delete a task with its subtasks (Asana keeps it in its trash for a while). " +
			"Only delete a task the user asked to delete. Call it with dry_run first to see what goes with it " +
			"(subtasks, unsent changes) and confirm with the user. Until the delete is sent the task is hidden, " +
			"and the user can still undo it by discarding the outbox entry (resolve_outbox)."}, s.deleteTask)
	mcp.AddTool(srv, &mcp.Tool{Name: "set_parent", Annotations: write(false),
		Description: "Make a task a subtask of another task (parent), or a top-level task again (parent empty). " +
			"insert_before / insert_after put it just before / after another subtask of the parent."}, s.setParent)
}

type DeleteTaskInput struct {
	GID    string `json:"gid" jsonschema:"task gid"`
	DryRun bool   `json:"dry_run,omitempty" jsonschema:"only tell what would be deleted; change nothing"`
}

type DeleteTaskOutput struct {
	WriteOutput
	DryRun bool `json:"dry_run,omitempty"`
	// Plan is what the delete takes with it.
	Plan readmodel.DeletePlan `json:"plan"`
}

type SetParentInput struct {
	GID          string `json:"gid" jsonschema:"task gid"`
	Parent       string `json:"parent" jsonschema:"gid of the new parent task; empty to make it a top-level task"`
	InsertBefore string `json:"insert_before,omitempty" jsonschema:"gid of a subtask of the parent to put it just before"`
	InsertAfter  string `json:"insert_after,omitempty" jsonschema:"gid of a subtask of the parent to put it just after"`
}

func (s *Server) deleteTask(ctx context.Context, _ *mcp.CallToolRequest, in DeleteTaskInput) (*mcp.CallToolResult, DeleteTaskOutput, error) {
	ops := app.TaskOps{Store: s.Store, Clock: s.Now}
	if in.DryRun {
		plan, err := ops.PlanDelete(ctx, in.GID)
		if err != nil {
			return nil, DeleteTaskOutput{}, err
		}
		return nil, DeleteTaskOutput{WriteOutput: WriteOutput{TaskGID: in.GID, Note: "dry run: nothing was changed"}, DryRun: true, Plan: plan}, nil
	}
	plan, err := ops.Delete(ctx, in.GID)
	if err != nil {
		return nil, DeleteTaskOutput{}, err
	}
	if plan.Local { // never sent: removed here, nothing to send
		return nil, DeleteTaskOutput{WriteOutput: WriteOutput{TaskGID: in.GID, Sent: true,
			Note: "the task was never sent to Asana; it was removed locally"}, Plan: plan}, nil
	}
	return nil, DeleteTaskOutput{WriteOutput: s.push(ctx, in.GID), Plan: plan}, nil
}

func (s *Server) setParent(ctx context.Context, _ *mcp.CallToolRequest, in SetParentInput) (*mcp.CallToolResult, WriteOutput, error) {
	ops := app.TaskOps{Store: s.Store, Clock: s.Now}
	if err := ops.SetParent(ctx, in.GID, in.Parent, model.Position{Before: in.InsertBefore, After: in.InsertAfter}); err != nil {
		return nil, WriteOutput{}, err
	}
	return nil, s.push(ctx, in.GID), nil
}
