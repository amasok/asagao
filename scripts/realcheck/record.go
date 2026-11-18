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
// and was made by this run: its name has the prefix, or (tasks) it is in one of our
// projects or under one of our tasks.
func (r *Runner) owned(ctx context.Context, c *Created, kind, gid string) (bool, string) {
	var fields string
	switch kind {
	case "tasks":
		fields = "name,workspace.gid,projects.gid,parent.gid"
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
		Projects []struct {
			GID string `json:"gid"`
		} `json:"projects"`
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
	if strings.HasPrefix(o.Name, c.Prefix) {
		return true, ""
	}
	if kind == "tasks" {
		for _, p := range o.Projects {
			if slices.Contains(c.Projects, p.GID) {
				return true, ""
			}
		}
		if o.Parent != nil && slices.Contains(c.Tasks, o.Parent.GID) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("name %q has no prefix %q", o.Name, c.Prefix)
}

// Cleanup deletes what the run created: tasks first (deleting a project leaves its tasks),
// then status updates, tags, custom fields and projects. It returns what was left behind.
func (r *Runner) Cleanup(ctx context.Context, c *Created) (left []string) {
	r.Close() // no MCP server pushing while we delete
	del := func(kind string, gids []string) {
		for i := len(gids) - 1; i >= 0; i-- { // subtasks and copies were added after parents
			g := gids[i]
			ok, why := r.owned(ctx, c, kind, g)
			if !ok {
				if strings.Contains(why, "Not Found") || strings.Contains(why, "Unknown object") || strings.Contains(why, "does not exist") {
					continue // already gone (deleted by the run, or with its parent)
				}
				left = append(left, fmt.Sprintf("%s/%s (not deleted: %s)", kind, g, why))
				continue
			}
			if _, err := r.WriteAPI(ctx, c, "DELETE", "/"+kind+"/"+g); err != nil {
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
