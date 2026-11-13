// Package sync pulls data from Asana into the local store.
package sync

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/config"
	"github.com/amasok/asana-tui/internal/store"
)

type Syncer struct {
	API   *asana.Client
	Store *store.Store
	Cfg   config.SyncConfig
	// Progress receives human-readable progress messages (optional).
	Progress func(format string, args ...any)
	now      func() time.Time
}

type Result struct {
	Workspaces int           `json:"workspaces"`
	Projects   int           `json:"projects"`
	Tasks      int           `json:"tasks"`
	Duration   time.Duration `json:"duration_ns"`
}

// Full fetches the user's tasks and the tasks of their projects in every workspace.
// Completed tasks older than Cfg.CompletedDays are skipped.
func (s *Syncer) Full(ctx context.Context) (Result, error) {
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	started := now()
	var res Result

	me, err := s.API.Me(ctx)
	if err != nil {
		return res, fmt.Errorf("ユーザー情報の取得: %w", err)
	}
	if err := s.Store.UpsertUser(ctx, *me); err != nil {
		return res, err
	}
	workspaces, err := s.API.Workspaces(ctx)
	if err != nil {
		return res, fmt.Errorf("ワークスペースの取得: %w", err)
	}
	if err := s.Store.UpsertWorkspaces(ctx, workspaces); err != nil {
		return res, err
	}
	res.Workspaces = len(workspaces)

	completedSince := started.AddDate(0, 0, -s.Cfg.CompletedDays)
	for _, w := range workspaces {
		s.progress("ワークスペース %s: 自分のタスク", w.Name)
		mine, err := s.API.Tasks(ctx, asana.TaskQuery{Assignee: "me", Workspace: w.GID, CompletedSince: completedSince})
		if err != nil {
			return res, fmt.Errorf("%s の担当タスクの取得: %w", w.Name, err)
		}
		if err := s.Store.UpsertTasks(ctx, mine); err != nil {
			return res, err
		}
		res.Tasks += len(mine)

		projects, err := s.API.Projects(ctx, w.GID, false)
		if err != nil {
			return res, fmt.Errorf("%s のプロジェクトの取得: %w", w.Name, err)
		}
		if err := s.Store.UpsertProjects(ctx, w.GID, projects, me.GID); err != nil {
			return res, err
		}
		for _, p := range projects {
			if !s.Cfg.AllProjects && !slices.ContainsFunc(p.Members, func(r asana.Ref) bool { return r.GID == me.GID }) {
				continue
			}
			s.progress("プロジェクト %s", p.Name)
			secs, err := s.API.Sections(ctx, p.GID)
			if err != nil {
				return res, fmt.Errorf("%s のセクションの取得: %w", p.Name, err)
			}
			if err := s.Store.ReplaceSections(ctx, p.GID, secs); err != nil {
				return res, err
			}
			tasks, err := s.API.Tasks(ctx, asana.TaskQuery{Project: p.GID, CompletedSince: completedSince})
			if err != nil {
				return res, fmt.Errorf("%s のタスクの取得: %w", p.Name, err)
			}
			if err := s.Store.UpsertTasks(ctx, tasks); err != nil {
				return res, err
			}
			res.Projects++
			res.Tasks += len(tasks)
		}
	}

	if err := s.Store.SetSyncState(ctx, store.KeyLastSync, started.UTC().Format(time.RFC3339)); err != nil {
		return res, err
	}
	res.Duration = now().Sub(started)
	return res, nil
}

func (s *Syncer) progress(format string, args ...any) {
	if s.Progress != nil {
		s.Progress(format, args...)
	}
}
