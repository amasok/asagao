package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/amasok/asagao/internal/asana/model"
	localgid "github.com/amasok/asagao/internal/domain/gid"
	"github.com/amasok/asagao/internal/readmodel"
)

// Finding in Asana what the local cache does not have (Issue #36): tasks by text, users and
// tags by name, a task by its custom ID. What is found is cached (a task stays until a full
// sync prunes it, like any task outside the synced projects).
//
// The search API needs a premium plan. When Asana answers 402, that is remembered for the
// workspace for readmodel.PlanRecheckAfter (readmodel.PlanKey with PlanSearch) and
// typeahead, which matches task names only, is used meanwhile. If typeahead too answers
// 402, it is remembered the same way: remote search then fails with
// ErrRemoteSearchUnavailable, and names are looked up in the cache only. After that time
// Asana is asked again, and an answer forgets the 402.

// ErrRemoteSearchUnavailable is returned by SearchTasks when the workspace's plan offers
// neither the search API nor typeahead.
var ErrRemoteSearchUnavailable = errors.New(readmodel.PlanUnavailableMessage(readmodel.PlanTypeahead))

// SearchResult is what SearchTasks found.
type SearchResult = readmodel.RemoteSearchResult

// typeaheadNames is how many users or tags one name lookup asks for.
const typeaheadNames = 10

// customIDSearchLimit is how many search candidates TaskByCustomID asks for when the
// custom ID endpoint answers 500 (Asana's maximum per search request).
const customIDSearchLimit = 100

// planAnswer records what Asana answered to feature in workspace ws (err): a 402 hides it
// there for a while, an answer forgets an earlier one. It reports whether err is a 402. A
// failure to record it is not returned: the feature is then only asked again.
func (s *Syncer) planAnswer(ctx context.Context, feature, ws string, err error) bool {
	if err == nil {
		_ = s.clearPlanRefusal(ctx, feature, ws)
		return false
	}
	refused, _ := s.recordPlanRefusal(ctx, feature, ws, err)
	return refused || model.IsPaymentRequired(err)
}

// SearchTasks searches Asana for tasks by text and caches what it finds. completed nil
// means open and completed tasks; limit is at most 100 (Asana's limit).
func (s *Syncer) SearchTasks(ctx context.Context, text string, completed *bool, limit int) (SearchResult, error) {
	var res SearchResult
	text = strings.TrimSpace(text)
	if text == "" {
		return res, errors.New("検索する語がありません")
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return res, err
	}
	refs, byName, err := s.searchRefs(ctx, ws, text, completed, limit)
	if err != nil {
		return res, err
	}
	res.ByName = byName
	gids := uniqueRemoteGIDs(refs)
	if len(gids) == 0 {
		return res, nil
	}
	// The found records are compact: read the tasks as a sync does, so that a cached task
	// is never overwritten with only a name.
	tasks, _, err := s.API.TasksByGID(ctx, gids)
	if err != nil {
		return res, fmt.Errorf("見つけたタスクを読めませんでした: %w", err)
	}
	byGID := make(map[string]model.Task, len(tasks))
	var keep []model.Task
	for _, t := range tasks {
		if completed != nil && t.Completed != *completed {
			continue // typeahead does not filter
		}
		byGID[t.GID] = t
		keep = append(keep, t)
	}
	if err := s.Store.UpsertTasks(ctx, keep); err != nil {
		return res, err
	}
	for _, g := range gids {
		if _, ok := byGID[g]; ok {
			res.GIDs = append(res.GIDs, g)
		}
	}
	return res, nil
}

// searchRefs asks the search API, or typeahead when the plan lacks it (byName).
func (s *Syncer) searchRefs(ctx context.Context, ws, text string, completed *bool, limit int) (refs []model.Ref, byName bool, err error) {
	if no, err := s.planHidden(ctx, readmodel.PlanSearch, ws); err != nil {
		return nil, false, err
	} else if !no {
		refs, err := s.API.SearchTasks(ctx, ws, text, completed, limit)
		if !s.planAnswer(ctx, readmodel.PlanSearch, ws, err) {
			return refs, false, err
		}
	}
	if no, err := s.planHidden(ctx, readmodel.PlanTypeahead, ws); err != nil {
		return nil, true, err
	} else if no {
		return nil, true, ErrRemoteSearchUnavailable
	}
	refs, err = s.API.TypeaheadTasks(ctx, ws, text, limit)
	if s.planAnswer(ctx, readmodel.PlanTypeahead, ws, err) {
		return nil, true, ErrRemoteSearchUnavailable
	}
	return refs, true, err
}

// FindUsers looks users up in Asana by the start of their name or email (typeahead) and
// caches the ones the cache does not have. It returns how many it added: 0 also when the
// plan lacks typeahead (then nothing is asked).
func (s *Syncer) FindUsers(ctx context.Context, query string) (int, error) {
	return findNames(ctx, s, query, s.API.TypeaheadUsers, func(ctx context.Context, _ string, found []model.User) (int, error) {
		return s.Store.AddUsers(ctx, found)
	})
}

// FindTags is FindUsers for tags.
func (s *Syncer) FindTags(ctx context.Context, query string) (int, error) {
	return findNames(ctx, s, query, s.API.TypeaheadTags, func(ctx context.Context, ws string, found []model.Tag) (int, error) {
		for i := range found {
			if found[i].Workspace == nil || found[i].Workspace.GID == "" {
				found[i].Workspace = &model.Ref{GID: ws}
			}
		}
		return s.Store.AddTags(ctx, found)
	})
}

func findNames[T any](ctx context.Context, s *Syncer, query string,
	find func(ctx context.Context, ws, query string, count int) ([]T, error),
	add func(ctx context.Context, ws string, found []T) (int, error)) (int, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return 0, nil
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return 0, err
	}
	if no, err := s.planHidden(ctx, readmodel.PlanTypeahead, ws); err != nil || no {
		return 0, err
	}
	found, err := find(ctx, ws, query, typeaheadNames)
	if s.planAnswer(ctx, readmodel.PlanTypeahead, ws, err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return add(ctx, ws, found)
}

// ErrCustomIDUnavailable is returned by TaskByCustomID when the workspace's plan does not
// give tasks by custom ID (Asana answered 402, now or before).
var ErrCustomIDUnavailable = errors.New(readmodel.PlanUnavailableMessage(readmodel.PlanCustomID))

// TaskByCustomID finds the task with the custom ID (e.g. "ENG-123") in Asana, caches it,
// remembers its gid (readmodel.KeyCustomID) and returns it. A custom ID Asana does not know
// is readmodel.ErrNotFound (and forgotten). A 402 is remembered for the workspace
// (PlanCustomID): Asana is not asked again there for readmodel.PlanRecheckAfter.
//
// When GET /tasks/custom_id/{id} answers 500 (Issue #65: real Asana workspaces return 500
// even for existing custom IDs), TaskByCustomID falls back to the search API (text=<ID>),
// reads candidate tasks in full and adopts only a task whose ID custom field equals customID.
func (s *Syncer) TaskByCustomID(ctx context.Context, customID string) (string, error) {
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return "", err
	}
	if no, err := s.planHidden(ctx, readmodel.PlanCustomID, ws); err != nil {
		return "", err
	} else if no {
		return "", ErrCustomIDUnavailable
	}
	key := readmodel.KeyCustomID + customID
	t, err := s.API.TaskByCustomID(ctx, ws, customID)
	var apiErr *model.Error
	if s.planAnswer(ctx, readmodel.PlanCustomID, ws, notFoundIsAnswer(err)) {
		return "", ErrCustomIDUnavailable
	}
	switch {
	case err == nil:
		if t == nil || t.GID == "" {
			_ = s.Store.SetSyncState(ctx, key, "")
			return "", fmt.Errorf("タスクの ID %q: %w", customID, readmodel.ErrNotFound)
		}
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
		_ = s.Store.SetSyncState(ctx, key, "")
		return "", fmt.Errorf("タスクの ID %q: %w", customID, readmodel.ErrNotFound)
	case errors.As(err, &apiErr) && apiErr.Status == http.StatusInternalServerError:
		t, err = s.taskByCustomIDFromSearch(ctx, ws, customID, err)
		if err != nil {
			return "", err
		}
		_ = s.clearPlanRefusal(ctx, readmodel.PlanCustomID, ws)
		if t == nil || t.GID == "" {
			_ = s.Store.SetSyncState(ctx, key, "")
			return "", fmt.Errorf("タスクの ID %q: %w", customID, readmodel.ErrNotFound)
		}
	default:
		return "", err
	}
	if err := s.Store.UpsertTasks(ctx, []model.Task{*t}); err != nil {
		return "", err
	}
	if err := s.Store.SetSyncState(ctx, key, t.GID); err != nil {
		return "", err
	}
	return t.GID, nil
}

// taskByCustomIDFromSearch resolves customID via the search API when the custom ID endpoint
// answered 500 (origErr). A nil task with a nil error means Asana answered with fewer than
// customIDSearchLimit results, all candidates had full fields, and none has customID. When
// the workspace's plan lacks the search API (402, now or before), PlanSearch is recorded as
// usual, PlanCustomID is left untouched, and origErr is returned. When no match is found
// and the search hit customIDSearchLimit (truncated) or any candidate was Partial (custom
// fields omitted), existence cannot be ruled out, so origErr is returned.
func (s *Syncer) taskByCustomIDFromSearch(ctx context.Context, ws, customID string, origErr error) (*model.Task, error) {
	if no, err := s.planHidden(ctx, readmodel.PlanSearch, ws); err != nil {
		return nil, err
	} else if no {
		return nil, origErr
	}
	refs, err := s.API.SearchTasks(ctx, ws, customID, nil, customIDSearchLimit)
	if s.planAnswer(ctx, readmodel.PlanSearch, ws, err) {
		return nil, origErr
	}
	if err != nil {
		return nil, err
	}
	gids := uniqueRemoteGIDs(refs)
	hadPartial := false
	for chunk := range slices.Chunk(gids, model.MaxBatchActions) {
		tasks, _, err := s.API.TasksByGID(ctx, chunk)
		if err != nil {
			return nil, err
		}
		for i := range tasks {
			if tasks[i].Partial {
				// Custom fields were left out: keep scanning in case a later candidate matches.
				hadPartial = true
				continue
			}
			if tasks[i].GID != "" && !localgid.IsLocal(tasks[i].GID) && tasks[i].HasCustomID(customID) {
				return &tasks[i], nil
			}
		}
	}
	if hadPartial || len(refs) >= customIDSearchLimit {
		return nil, origErr
	}
	return nil, nil
}

func uniqueRemoteGIDs(refs []model.Ref) []string {
	var gids []string
	seen := map[string]bool{}
	for _, r := range refs {
		if r.GID == "" || seen[r.GID] || localgid.IsLocal(r.GID) {
			continue
		}
		seen[r.GID] = true
		gids = append(gids, r.GID)
	}
	return gids
}

// notFoundIsAnswer treats a 404 as an answer for the plan (Asana offers the feature; it
// only does not know the thing asked for).
func notFoundIsAnswer(err error) error {
	var apiErr *model.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	return err
}
