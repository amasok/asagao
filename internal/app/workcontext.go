package app

import (
	"context"
	"strings"

	"github.com/amasok/asagao/internal/repoctx"
)

// WorkContext is a .asana.json (repoctx.Context) with what it names looked up in the
// cache (LookupWorkContext), for `asagao context` and the MCP tool get_context.
type WorkContext struct {
	Path    string       `json:"path"`
	Project *ContextRef  `json:"project,omitempty"`
	Section *ContextRef  `json:"section,omitempty"`
	Task    *ContextTask `json:"task,omitempty"`
}

// ContextRef is one entry of a .asana.json: what is written, and what it is in the cache.
type ContextRef struct {
	// Value is what the file says (a gid or a name).
	Value string `json:"value"`
	GID   string `json:"gid,omitempty"`
	Name  string `json:"name,omitempty"`
	// Error says why it was not found in the cache (not synced yet, a typo, ...).
	Error string `json:"error,omitempty"`
}

// ContextTask is the task of a .asana.json.
type ContextTask struct {
	ContextRef
	Completed    bool   `json:"completed"`
	DueOn        string `json:"due_on,omitempty"`
	PermalinkURL string `json:"permalink_url,omitempty"`
}

// LookupWorkContext looks up the project, section and task of c in the cache with q. What
// is not found is kept with its Error: the file is still shown as it is. (Not a method of
// Query: Query's methods are the cache's reads as they are.)
func LookupWorkContext(ctx context.Context, q Query, c *repoctx.Context) WorkContext {
	w := WorkContext{Path: c.Path}
	if c.Project != "" {
		w.Project = &ContextRef{Value: c.Project}
		if gid, err := q.ResolveProject(ctx, c.Project); err != nil {
			w.Project.Error = err.Error()
		} else {
			w.Project.GID = gid
		}
	}
	if c.Section != "" {
		w.Section = &ContextRef{Value: c.Section}
	}
	if w.Project != nil && w.Project.GID != "" {
		contextProject(ctx, q, w.Project, w.Section)
	}
	if c.Task != "" {
		w.Task = &ContextTask{ContextRef: ContextRef{Value: c.Task}}
		if d, err := q.GetTask(ctx, c.Task); err != nil {
			w.Task.Error = err.Error()
		} else {
			w.Task.GID, w.Task.Name = d.GID, d.Name
			w.Task.Completed, w.Task.DueOn, w.Task.PermalinkURL = d.Completed, d.DueOn, d.PermalinkURL
		}
	}
	return w
}

// contextProject fills in the name of project p and finds section s (when not nil) in it:
// by gid, by name, or by a part of the name that only one section has.
func contextProject(ctx context.Context, q Query, p, s *ContextRef) {
	ps, err := q.ListProjects(ctx, false)
	if err != nil {
		p.Error = err.Error()
		return
	}
	for _, pv := range ps {
		if pv.GID != p.GID {
			continue
		}
		p.Name = pv.Name
		if s == nil {
			return
		}
		var partial []int
		for i, sv := range pv.Sections {
			if sv.GID == s.Value || strings.EqualFold(sv.Name, s.Value) {
				s.GID, s.Name = sv.GID, sv.Name
				return
			}
			if strings.Contains(strings.ToLower(sv.Name), strings.ToLower(s.Value)) {
				partial = append(partial, i)
			}
		}
		if len(partial) == 1 {
			s.GID, s.Name = pv.Sections[partial[0]].GID, pv.Sections[partial[0]].Name
			return
		}
		s.Error = "プロジェクト「" + pv.Name + "」にセクション「" + s.Value + "」が見つかりません"
		return
	}
	if s != nil {
		s.Error = "プロジェクトがキャッシュにありません"
	}
}
