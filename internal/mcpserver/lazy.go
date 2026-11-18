package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/sync"
)

// Projects whose tasks are fetched when they are opened (#54): a tool that lists a
// project's tasks fetches them first if they are not cached.

// openTimeout bounds fetching a project's tasks for a tool.
const openTimeout = 2 * time.Minute

// openProject makes sure a project's tasks are cached before they are listed: it records
// the project as one to sync and, if its tasks are not cached, fetches them now (with the
// completed ones). It returns a note for the agent when tasks are still missing, and what
// was fetched (nil if nothing).
func (s *Server) openProject(ctx context.Context, gid string) (string, *sync.OpenResult) {
	state, err := s.Store.WantProject(ctx, gid)
	if err != nil || state == readmodel.TasksAll {
		return "", nil
	}
	var fetched *sync.OpenResult
	syncer, err := s.getSyncer(ctx)
	if err == nil {
		octx, cancel := context.WithTimeout(ctx, openTimeout)
		res, oerr := syncer.OpenProject(octx, gid, true)
		cancel()
		if oerr != nil {
			s.dropSyncerOn(oerr)
			err = oerr
		} else {
			state = res.State
			if res.Fetched {
				fetched = &res
			}
		}
	}
	if err == nil && state == readmodel.TasksAll {
		return "", fetched
	}
	what := "this project's tasks are"
	if state == readmodel.TasksOpen {
		what = "this project's completed tasks are"
	}
	if err != nil {
		return fmt.Sprintf("%s not in the local cache yet and could not be fetched now (%v); the next sync fetches them. "+
			"Only tasks cached otherwise (e.g. the user's own) are listed.", what, err), fetched
	}
	return what + " not in the local cache yet; the next sync fetches them.", fetched
}

// unfetchedNote says how many projects a search of the cache did not look in.
func (s *Server) unfetchedNote(ctx context.Context) string {
	n, err := s.Store.CountUnfetchedProjects(ctx, true)
	if err != nil || n == 0 {
		return ""
	}
	return fmt.Sprintf("%d of the user's projects are not cached yet (their tasks are fetched once a project is opened, "+
		"e.g. by list_tasks with that project), so they were not searched; remote=true searches Asana too.", n)
}
