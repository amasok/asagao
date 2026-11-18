package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"
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
	// Made by -shapes.
	TimeEntries      []string
	Allocations      []string
	ProjectTemplates []string
	Goals            []string
	Portfolios       []string
	Jobs             []string // duplicate / template jobs: cleanup waits for them
	// Deleted are "kind/gid" the run deleted itself (a deleted goal answers 403, not 404).
	Deleted []string
}

// recordJob records what a job (its start or a GET /jobs) says it makes, as soon as it
// says so, so cleanup deletes it even when the job is slow or the run stops.
func (c *Created) recordJob(v any) {
	if g := str(v, "data", "new_project", "gid"); gidRE.MatchString(g) {
		c.add(&c.Projects, g)
	}
	if g := str(v, "data", "new_project_template", "gid"); gidRE.MatchString(g) {
		c.add(&c.ProjectTemplates, g)
	}
	if g := str(v, "data", "new_task", "gid"); gidRE.MatchString(g) {
		c.add(&c.Tasks, g)
	}
}

// addPrefixed adds the typeahead results whose name has the prefix to list.
func (c *Created) addPrefixed(list *[]string, data json.RawMessage) {
	var a []struct {
		GID  string `json:"gid"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(data, &a)
	for _, e := range a {
		if strings.HasPrefix(e.Name, c.Prefix) && gidRE.MatchString(e.GID) {
			c.add(list, e.GID)
		}
	}
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
		case "time_tracking_entries":
			list = c.TimeEntries
		case "allocations":
			list = c.Allocations
		case "project_templates":
			list = c.ProjectTemplates
		case "goals":
			list = c.Goals
		case "portfolios":
			list = c.Portfolios
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
	case "time_tracking_entries":
		fields = "task.gid"
	case "allocations":
		fields = "parent.gid"
	case "project_templates":
		fields = "name"
	default:
		fields = "name,workspace.gid"
	}
	data, err := r.API(ctx, "GET", "/"+kind+"/"+gid, "opt_fields="+fields)
	if err != nil {
		return false, err.Error()
	}
	var o struct {
		Task *struct {
			GID string `json:"gid"`
		} `json:"task"`
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
	switch kind {
	case "status_updates", "allocations":
		if o.Parent != nil && slices.Contains(c.Projects, o.Parent.GID) {
			return true, ""
		}
		return false, kind + " not on our project"
	case "time_tracking_entries":
		if o.Task != nil && slices.Contains(c.Tasks, o.Task.GID) {
			return true, ""
		}
		return false, "time entry not on our task"
	case "project_templates":
		if strings.HasPrefix(o.Name, c.Prefix) {
			return true, "" // templates have a team, not a workspace field
		}
		return false, fmt.Sprintf("name %q has no prefix %q", o.Name, c.Prefix)
	}
	// A custom field has no workspace to ask for; its gid was recorded when this run made it.
	if kind != "custom_fields" && o.Workspace.GID != TestWorkspace {
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
	typeahead := func(kind string, list *[]string) {
		data, err := r.API(ctx, "GET", "/workspaces/"+TestWorkspace+"/typeahead",
			"resource_type="+kind, "query="+c.Prefix, "count=100", "opt_fields=name")
		if err == nil {
			c.addPrefixed(list, data)
		}
	}
	// Projects first: their tasks are collected next (a copy made by a slow job).
	typeahead("project", &c.Projects)
	typeahead("project_template", &c.ProjectTemplates)
	typeahead("goal", &c.Goals)
	typeahead("portfolio", &c.Portfolios)
	gidsOf := func(data json.RawMessage) []string {
		var a []struct {
			GID string `json:"gid"`
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
	typeahead("task", &c.Tasks)
	typeahead("tag", &c.Tags)
}

// waitJobs waits (up to 2 minutes) for the jobs the run started, so a duplicate or a
// template is not still being made while cleanup deletes; what they made is recorded.
func (r *Runner) waitJobs(ctx context.Context, c *Created) {
	deadline := time.Now().Add(2 * time.Minute)
	for _, j := range c.Jobs {
		for time.Now().Before(deadline) {
			data, err := r.API(ctx, "GET", "/jobs/"+j)
			if err != nil {
				break
			}
			v := map[string]any{"data": decode(data)}
			c.recordJob(v)
			if st := str(v, "data", "status"); st == "succeeded" || st == "failed" {
				break
			}
			time.Sleep(3 * time.Second)
		}
	}
}

// gone reports whether an error from GET means the object does not exist (any more).
func gone(why string) bool {
	for _, s := range []string{"Not Found", "Unknown object", "does not exist", "not found", "Not a recognized ID"} {
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
	r.waitJobs(ctx, c)
	r.collect(ctx, c)
	del := func(kind string, gids []string) {
		for i := len(gids) - 1; i >= 0; i-- { // subtasks and copies were added after parents
			g := gids[i]
			if slices.Contains(c.Deleted, kind+"/"+g) {
				continue
			}
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
	del("time_tracking_entries", c.TimeEntries)
	del("allocations", c.Allocations)
	del("project_templates", c.ProjectTemplates)
	del("portfolios", c.Portfolios)
	del("goals", c.Goals)
	del("projects", c.Projects)
	return left
}

// guardBody allows a write with a JSON body (`asagao api --input`) only on what this run
// created; whatever it creates must carry the prefix (or belong to our project / task).
func (c *Created) guardBody(method, path string, body M) error {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	data, _ := body["data"].(M)
	name, _ := data["name"].(string)
	prefixed := strings.HasPrefix(name, c.Prefix)
	ours := func(list []string, i int) bool {
		return len(parts) > i && gidRE.MatchString(parts[i]) && slices.Contains(list, parts[i])
	}
	ok := false
	switch {
	case method == "POST" && path == "/tags":
		ok = data["workspace"] == TestWorkspace && prefixed
	case method == "PUT" && len(parts) == 2 && (parts[0] == "tags" || parts[0] == "projects"):
		// only the color (the color-name probe); other edits go through asagao
		list := map[string][]string{"tags": c.Tags, "projects": c.Projects}[parts[0]]
		_, color := data["color"]
		ok = ours(list, 1) && len(data) == 1 && color
	case method == "PUT" && len(parts) == 2 && parts[0] == "tasks":
		ok = ours(c.Tasks, 1)
	case method == "POST" && len(parts) == 3 && parts[0] == "tasks" &&
		(parts[2] == "time_tracking_entries" || parts[2] == "addDependencies" || parts[2] == "removeDependencies"):
		ok = ours(c.Tasks, 1)
	case (method == "PUT" || method == "DELETE") && len(parts) == 2 && parts[0] == "time_tracking_entries":
		ok = ours(c.TimeEntries, 1)
	case method == "POST" && path == "/allocations":
		ok = slices.Contains(c.Projects, fmt.Sprint(data["parent"])) && data["assignee"] == TestUser
	case method == "PUT" && len(parts) == 2 && parts[0] == "allocations":
		ok = ours(c.Allocations, 1)
	case method == "POST" && len(parts) == 3 && parts[0] == "custom_fields" && parts[2] == "enum_options":
		ok = ours(c.CustomFields, 1) && prefixed
	case method == "POST" && path == "/custom_fields":
		ok = data["workspace"] == TestWorkspace && prefixed
	case method == "POST" && len(parts) == 3 && parts[0] == "projects" &&
		(parts[2] == "addCustomFieldSetting" || parts[2] == "removeCustomFieldSetting"):
		ok = ours(c.Projects, 1)
	case method == "POST" && len(parts) == 3 && parts[0] == "projects" && (parts[2] == "saveAsTemplate" || parts[2] == "duplicate"):
		ok = ours(c.Projects, 1) && prefixed && data["public"] != true
	case method == "POST" && path == "/goals":
		ok = data["workspace"] == TestWorkspace && data["team"] == TestTeam && prefixed
	case method == "POST" && len(parts) == 3 && parts[0] == "goals" && (parts[2] == "setMetric" || parts[2] == "setMetricCurrentValue"):
		ok = ours(c.Goals, 1)
	case method == "POST" && path == "/portfolios":
		ok = data["workspace"] == TestWorkspace && data["public"] == false && prefixed
	case method == "POST" && len(parts) == 3 && parts[0] == "portfolios" && (parts[2] == "addItem" || parts[2] == "removeItem"):
		item := fmt.Sprint(data["item"])
		ok = ours(c.Portfolios, 1) && (slices.Contains(c.Projects, item) || slices.Contains(c.Portfolios, item))
	case method == "POST" && len(parts) == 3 && parts[0] == "goals" &&
		(parts[2] == "addSupportingRelationship" || parts[2] == "removeSupportingRelationship"):
		res := fmt.Sprint(data["supporting_resource"])
		ok = ours(c.Goals, 1) && (slices.Contains(c.Goals, res) || slices.Contains(c.Projects, res))
	case method == "POST" && len(parts) == 3 && parts[0] == "project_templates" && parts[2] == "instantiateProject":
		ok = ours(c.ProjectTemplates, 1) && prefixed && data["public"] != true
	}
	if !ok {
		return fmt.Errorf("refusing %s %s: not something this run created", method, path)
	}
	return nil
}

// WriteBody sends body as is (`asagao api --input`) behind guardBody and returns the raw
// output (Asana's JSON, also on errors) for the shape files.
func (r *Runner) WriteBody(ctx context.Context, c *Created, method, path string, body M) ([]byte, error) {
	if strings.Contains(path, "?") {
		return nil, fmt.Errorf("refusing %s %s: no query on writes", method, path)
	}
	if err := c.guardBody(method, path, body); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(r.Env.Work, "body-*.json")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if err := json.NewEncoder(f).Encode(body); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	return r.CLI(ctx, "api", "-X", method, path, "--input", f.Name())
}
