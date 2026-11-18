package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Created is everything the run made in Asana, so cleanup deletes exactly that and nothing
// else. Writes through `asagao api` are allowed only on these gids (see guardWrite).
type Created struct {
	Prefix       string // every name starts with it: asagao-realcheck-<time>
	Projects     []string
	Tasks        []string
	Tags         []string
	Statuses     []string
	CustomFields []string
}

func (c *Created) add(list *[]string, gid string) {
	if gid != "" && !slices.Contains(*list, gid) {
		*list = append(*list, gid)
	}
}

var gidRE = regexp.MustCompile(`^[0-9]{6,20}$`)

// guardWrite allows a write through `asagao api` only on what this run created, or to
// create a custom field in the test workspace.
func (c *Created) guardWrite(method, path string, fields []string) error {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case method == "DELETE" && len(parts) == 2 && gidRE.MatchString(parts[1]):
		var list []string
		switch parts[0] {
		case "tasks":
			list = c.Tasks
		case "projects":
			list = c.Projects
		case "tags":
			list = c.Tags
		case "status_updates":
			list = c.Statuses
		case "custom_fields":
			list = c.CustomFields
		}
		if slices.Contains(list, parts[1]) {
			return nil
		}
	case method == "POST" && len(parts) == 3 && parts[0] == "projects" &&
		(parts[2] == "addCustomFieldSetting" || parts[2] == "removeCustomFieldSetting"):
		if slices.Contains(c.Projects, parts[1]) {
			return nil
		}
	case method == "POST" && path == "/custom_fields":
		if slices.Contains(fields, "workspace="+TestWorkspace) && hasNamePrefix(fields, c.Prefix) {
			return nil
		}
	}
	return fmt.Errorf("refusing %s %s: not something this run created", method, path)
}

func hasNamePrefix(fields []string, prefix string) bool {
	for _, f := range fields {
		if v, ok := strings.CutPrefix(f, "name="); ok && strings.HasPrefix(v, prefix) {
			return true
		}
	}
	return false
}

// WriteAPI is API for POST / PUT / DELETE, behind guardWrite.
func (r *Runner) WriteAPI(ctx context.Context, c *Created, method, path string, fields ...string) (json.RawMessage, error) {
	if err := c.guardWrite(method, strings.SplitN(path, "?", 2)[0], fields); err != nil {
		return nil, err
	}
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("refusing %s %s: no query on writes", method, path)
	}
	return r.API(ctx, method, path, fields...)
}

// owned checks in Asana, right before deleting, that an object is in the test workspace
// and has this run's prefix (tasks, tags, custom fields), or (status updates) is on one of
// our projects. The callers pass only gids this run recorded or collected from its own
// projects and tasks.
func (r *Runner) owned(ctx context.Context, c *Created, kind, gid string) (bool, string) {
	var fields string
	switch kind {
	case "status_updates":
		fields = "title,parent.gid"
	default:
		fields = "name,workspace.gid"
	}
	data, err := r.API(ctx, "GET", "/"+kind+"/"+gid, "opt_fields="+fields)
	if err != nil {
		return false, err.Error()
	}
	var o struct {
		Name      string `json:"name"`
		Workspace struct {
			GID string `json:"gid"`
		} `json:"workspace"`
		Parent *struct {
			GID string `json:"gid"`
		} `json:"parent"`
	}
	if err := json.Unmarshal(data, &o); err != nil {
		return false, err.Error()
	}
	if kind == "status_updates" {
		if o.Parent != nil && slices.Contains(c.Projects, o.Parent.GID) {
			return true, ""
		}
		return false, "status update not on our project"
	}
	if o.Workspace.GID != TestWorkspace {
		return false, "workspace " + o.Workspace.GID
	}
	if !strings.HasPrefix(o.Name, c.Prefix) {
		return false, fmt.Sprintf("name %q has no prefix %q", o.Name, c.Prefix)
	}
	return true, ""
}

// collect adds to c the tasks and tags of this run that were not recorded: every task of
// our projects and their subtasks (recursively), and what typeahead finds by the prefix
// (a copy made by a job, a task adopted by the reconciliation, ...). owned still checks
// each one before it is deleted.
func (r *Runner) collect(ctx context.Context, c *Created) {
	gidsOf := func(data json.RawMessage) []string {
		var a []struct {
			GID  string `json:"gid"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(data, &a)
		var out []string
		for _, e := range a {
			out = append(out, e.GID)
		}
		return out
	}
	for _, p := range c.Projects {
		if data, err := r.API(ctx, "GET", "/projects/"+p+"/tasks", "opt_fields=gid"); err == nil {
			for _, g := range gidsOf(data) {
				c.add(&c.Tasks, g)
			}
		}
	}
	for i := 0; i < len(c.Tasks); i++ { // c.Tasks grows: subtasks of subtasks
		if data, err := r.API(ctx, "GET", "/tasks/"+c.Tasks[i]+"/subtasks", "opt_fields=gid"); err == nil {
			for _, g := range gidsOf(data) {
				c.add(&c.Tasks, g)
			}
		}
	}
	for _, rt := range []struct {
		kind string
		list *[]string
	}{{"task", &c.Tasks}, {"tag", &c.Tags}} {
		data, err := r.API(ctx, "GET", "/workspaces/"+TestWorkspace+"/typeahead",
			"resource_type="+rt.kind, "query="+c.Prefix, "count=100", "opt_fields=name")
		if err != nil {
			continue
		}
		var a []struct {
			GID  string `json:"gid"`
			Name string `json:"name"`
		}
		_ = json.Unmarshal(data, &a)
		for _, e := range a {
			if strings.HasPrefix(e.Name, c.Prefix) {
				c.add(rt.list, e.GID)
			}
		}
	}
}

// gone reports whether an error from GET means the object does not exist (any more).
func gone(why string) bool {
	for _, s := range []string{"Not Found", "Unknown object", "does not exist", "not found"} {
		if strings.Contains(why, s) {
			return true
		}
	}
	return false
}

// Cleanup deletes what the run created: tasks first (deleting a project leaves its tasks),
// then status updates, tags, custom fields and projects. It returns what was left behind.
func (r *Runner) Cleanup(ctx context.Context, c *Created) (left []string) {
	r.Close() // no MCP server pushing while we delete
	r.collect(ctx, c)
	del := func(kind string, gids []string) {
		for i := len(gids) - 1; i >= 0; i-- { // subtasks and copies were added after parents
			g := gids[i]
			ok, why := r.owned(ctx, c, kind, g)
			if !ok {
				if gone(why) {
					continue // already gone (deleted by the run, or with its parent)
				}
				left = append(left, fmt.Sprintf("%s/%s (not deleted: %s)", kind, g, why))
				continue
			}
			if _, err := r.WriteAPI(ctx, c, "DELETE", "/"+kind+"/"+g); err != nil && !gone(err.Error()) {
				left = append(left, fmt.Sprintf("%s/%s (%v)", kind, g, err))
			}
		}
	}
	del("tasks", c.Tasks)
	del("status_updates", c.Statuses)
	del("tags", c.Tags)
	del("custom_fields", c.CustomFields)
	del("projects", c.Projects)
	return left
}
