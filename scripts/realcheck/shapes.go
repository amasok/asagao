package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Shapes are the raw responses of the paid features, saved for the fake servers of the
// issues that build on them (#2 templates, #6 time tracking, #7 goals / portfolios,
// #37 custom fields). Each file is the JSON Asana returned (errors included), with the
// request on the first lines.
type shapes struct {
	c   *Check
	dir string
	n   int
}

func (s *shapes) save(name, req string, out []byte, err error) any {
	s.n++
	body := strings.TrimSpace(string(out))
	if body == "" && err != nil {
		body = fmt.Sprintf("%q", err.Error())
	}
	file := filepath.Join(s.dir, fmt.Sprintf("%02d-%s.json", s.n, name))
	text := fmt.Sprintf("// %s\n%s\n", req, body)
	if werr := os.WriteFile(file, []byte(text), 0o644); werr != nil {
		s.c.Rep.fail("shape "+name, werr.Error())
	}
	var v any
	_ = json.Unmarshal(out, &v)
	return v
}

// get saves a GET (fields as query).
func (s *shapes) get(name, path string, fields ...string) any {
	args := []string{"api", path}
	for _, f := range fields {
		args = append(args, "-f", f)
	}
	out, err := s.c.R.CLI(s.c.ctx, args...)
	return s.save(name, "GET "+path+" "+strings.Join(fields, " "), out, err)
}

// write saves a write with a JSON body (behind guardBody).
func (s *shapes) write(name, method, path string, body M) any {
	s.c.gate()
	out, err := s.c.R.WriteBody(s.c.ctx, s.c.Made, method, path, body)
	return s.save(name, method+" "+path+" "+show(body), out, err)
}

// del saves a DELETE (behind guardWrite).
func (s *shapes) del(name, path string) {
	s.c.gate()
	if err := s.c.Made.guardWrite("DELETE", path, nil); err != nil {
		s.c.Rep.fail("shape "+name, err.Error())
		return
	}
	out, err := s.c.R.CLI(s.c.ctx, "api", "-X", "DELETE", path)
	s.save(name, "DELETE "+path, out, err)
}

// job polls GET /jobs/{gid} until it is done, saving the first and the last answer.
func (s *shapes) job(name string, started any) any {
	g := str(started, "data", "gid")
	s.c.Made.recordJob(started)
	if g == "" {
		return nil
	}
	s.c.Made.add(&s.c.Made.Jobs, g)
	var last any
	for i := 0; i < 30; i++ {
		out, err := s.c.R.CLI(s.c.ctx, "api", "/jobs/"+g)
		var v any
		_ = json.Unmarshal(out, &v)
		s.c.Made.recordJob(v) // the new project / template is known before the job ends
		if i == 0 {
			s.save(name+"-job-first", "GET /jobs/"+g, out, err)
		}
		last = v
		if st := str(v, "data", "status"); st == "succeeded" || st == "failed" || err != nil {
			b, _ := json.Marshal(v)
			s.save(name+"-job-done", "GET /jobs/"+g, b, err)
			return v
		}
		time.Sleep(2 * time.Second)
	}
	s.c.Rep.fail("job "+name+" が 60 秒で終わらない", show(last))
	return last
}

func (c *Check) shapes(templates bool) {
	c.group("本物のレスポンスの形 (-shapes)")
	s := &shapes{c: c, dir: filepath.Join(c.R.Env.Run, "shapes")}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		c.Rep.fail("shapes", err.Error())
		return
	}
	pre, ws := c.Made.Prefix, "/workspaces/"+TestWorkspace

	// #2 templates
	s.get("task-templates", "/task_templates", "project="+c.p)
	s.get("task-templates-fields", "/task_templates", "project="+c.p, "opt_fields=name,project,template")
	s.get("task-template-404", "/task_templates/1")
	s.get("project-templates-team", "/project_templates", "team="+c.team)
	s.get("project-templates-workspace", "/project_templates", "workspace="+TestWorkspace)
	s.get("project-template-404", "/project_templates/1")
	if templates {
		job := s.write("save-as-template", "POST", "/projects/"+c.p+"/saveAsTemplate",
			M{"data": M{"name": pre + "-template", "team": c.team, "public": false}})
		done := s.job("save-as-template", job)
		if tg := str(done, "data", "new_project_template", "gid"); tg != "" {
			c.Made.add(&c.Made.ProjectTemplates, tg)
			tmpl := s.get("project-template", "/project_templates/"+tg)
			s.get("project-templates-team-after", "/project_templates", "team="+c.team, "opt_fields=name,team,requested_dates,requested_roles,owner,public,color")
			job := s.write("instantiate-no-dates", "POST", "/project_templates/"+tg+"/instantiateProject",
				M{"data": M{"name": pre + "-from-template", "team": c.team, "public": false}})
			if d := s.job("instantiate-no-dates", job); str(d, "data", "new_project", "gid") != "" {
				c.Made.add(&c.Made.Projects, str(d, "data", "new_project", "gid"))
			}
			var dates []M
			for _, rd := range asArray(get(tmpl, "data", "requested_dates")) {
				dates = append(dates, M{"gid": str(rd, "gid"), "value": "2030-04-01"})
			}
			if len(dates) > 0 {
				job := s.write("instantiate-with-dates", "POST", "/project_templates/"+tg+"/instantiateProject",
					M{"data": M{"name": pre + "-from-template-dated", "team": c.team, "public": false, "requested_dates": dates}})
				if d := s.job("instantiate-with-dates", job); str(d, "data", "new_project", "gid") != "" {
					c.Made.add(&c.Made.Projects, str(d, "data", "new_project", "gid"))
				}
			}
		}
	}
	job := s.write("duplicate-project", "POST", "/projects/"+c.p+"/duplicate",
		M{"data": M{"name": pre + "-duplicate", "team": c.team, "include": "notes,task_notes,task_subtasks,task_dates"}})
	if d := s.job("duplicate-project", job); str(d, "data", "new_project", "gid") != "" {
		c.Made.add(&c.Made.Projects, str(d, "data", "new_project", "gid"))
	}

	// #37 custom fields
	enum := s.write("custom-field-enum-create", "POST", "/custom_fields",
		M{"data": M{"workspace": TestWorkspace, "name": pre + "-enum", "resource_subtype": "enum",
			"enum_options": []M{{"name": pre + "-a"}}}})
	if eg := str(enum, "data", "gid"); eg != "" {
		c.Made.add(&c.Made.CustomFields, eg)
		s.write("enum-option-add", "POST", "/custom_fields/"+eg+"/enum_options", M{"data": M{"name": pre + "-b"}})
		s.write("enum-option-add-same-name", "POST", "/custom_fields/"+eg+"/enum_options", M{"data": M{"name": pre + "-b"}})
		for _, step := range []string{"addCustomFieldSetting", "addCustomFieldSetting", "removeCustomFieldSetting", "removeCustomFieldSetting"} {
			s.write(step, "POST", "/projects/"+c.p+"/"+step, M{"data": M{"custom_field": eg}})
		}
	}
	s.get("workspace-custom-fields-page", ws+"/custom_fields", "opt_fields=name,resource_subtype,enum_options.name,enum_options.enabled", "limit=1")
	for k, g := range c.cf {
		var forms []any
		switch k {
		case "date":
			forms = []any{M{"date": "2030-05-01"}, "2030-05-02", nil}
		case "number":
			forms = []any{7, "8", nil}
		case "people":
			forms = []any{[]string{c.Me}, c.Me, nil}
		}
		for i, v := range forms {
			s.write(fmt.Sprintf("put-task-custom-field-%s-%d", k, i), "PUT", "/tasks/"+c.t1, M{"data": M{"custom_fields": M{g: v}}})
		}
	}

	// #6 time tracking
	ttFields := "opt_fields=duration_minutes,entered_on,created_by.name,attributable_to.name,created_at,description,billable_status,approval_status,category"
	s.get("time-entries-empty", "/tasks/"+c.t1+"/time_tracking_entries")
	s.get("time-entries-empty-fields", "/tasks/"+c.t1+"/time_tracking_entries", ttFields)
	te := s.write("time-entry-create", "POST", "/tasks/"+c.t1+"/time_tracking_entries", M{"data": M{"duration_minutes": 15, "entered_on": "2026-11-18"}})
	if tg := str(te, "data", "gid"); tg != "" {
		c.Made.add(&c.Made.TimeEntries, tg)
		s.get("time-entries-one", "/tasks/"+c.t1+"/time_tracking_entries")
		s.get("time-entries-one-fields", "/tasks/"+c.t1+"/time_tracking_entries", ttFields)
		s.get("time-entry", "/time_tracking_entries/"+tg)
		s.write("time-entry-update", "PUT", "/time_tracking_entries/"+tg, M{"data": M{"duration_minutes": 30}})
		s.del("time-entry-delete", "/time_tracking_entries/"+tg)
	}
	s.get("time-tracking-categories", ws+"/time_tracking_categories")
	s.get("time-tracking-entries-workspace", "/time_tracking_entries", "workspace="+TestWorkspace)

	// #7 goals, portfolios, time periods; #6 allocations
	s.get("goals", "/goals", "workspace="+TestWorkspace, "opt_fields=name,owner.name,status,due_on,start_on,time_period.display_name,current_status_update.title,metric.current_display_value,metric.target_value,metric.unit,metric.progress_source,html_notes,team.name,is_workspace_level,liked,num_likes")
	s.get("goal-404", "/goals/1")
	goalFields := "opt_fields=name,owner.name,status,due_on,start_on,time_period.display_name,current_status_update.title,metric,metric.current_display_value,metric.target_value,metric.unit,metric.progress_source,html_notes,notes,team.name,workspace.gid,is_workspace_level,liked,num_likes,followers.name"
	goal := s.write("goal-create", "POST", "/goals", M{"data": M{"name": pre + "-goal", "workspace": TestWorkspace, "team": c.team,
		"owner": c.Me, "due_on": "2030-06-30", "start_on": "2030-01-01", "notes": "dummy goal"}})
	if gg := str(goal, "data", "gid"); gg != "" {
		c.Made.add(&c.Made.Goals, gg)
		s.get("goal", "/goals/"+gg, goalFields)
		s.write("goal-set-metric", "POST", "/goals/"+gg+"/setMetric", M{"data": M{"metric_type": "number", "unit": "none", "precision": 0,
			"initial_number_value": 0, "target_number_value": 10, "progress_source": "manual"}})
		s.get("goal-with-metric", "/goals/"+gg, goalFields)
		s.get("goals-one", "/goals", "workspace="+TestWorkspace, "team="+c.team, goalFields)
		s.del("goal-delete", "/goals/"+gg)
	}
	s.get("portfolios", "/portfolios", "workspace="+TestWorkspace, "owner="+c.Me, "opt_fields=name,owner.name,color,current_status_update.title,current_status_update.status_type,due_on,start_on,permalink_url")
	s.get("portfolio-404", "/portfolios/1")
	pf := s.write("portfolio-create", "POST", "/portfolios", M{"data": M{"name": pre + "-portfolio", "workspace": TestWorkspace, "public": false, "color": "light-green"}})
	if pg := str(pf, "data", "gid"); pg != "" {
		c.Made.add(&c.Made.Portfolios, pg)
		s.write("portfolio-add-item", "POST", "/portfolios/"+pg+"/addItem", M{"data": M{"item": c.p}})
		s.get("portfolio", "/portfolios/"+pg, "opt_fields=name,owner.name,color,current_status_update.title,current_status_update.status_type,due_on,start_on,permalink_url,public,members.name,workspace.gid")
		s.get("portfolio-items", "/portfolios/"+pg+"/items", "opt_fields=name,resource_type,current_status_update.status_type,due_on,owner.name")
		s.get("portfolios-one", "/portfolios", "workspace="+TestWorkspace, "owner="+c.Me, "opt_fields=name,owner.name,color,current_status_update.title,current_status_update.status_type,due_on,start_on,permalink_url")
		s.write("portfolio-remove-item", "POST", "/portfolios/"+pg+"/removeItem", M{"data": M{"item": c.p}})
		s.del("portfolio-delete", "/portfolios/"+pg)
	}
	s.get("time-periods", "/time_periods", "workspace="+TestWorkspace, "opt_fields=display_name,start_on,end_on,period,parent")
	alFields := "opt_fields=start_date,end_date,effort,assignee.name,parent.name,resource_subtype"
	s.get("allocations-empty", "/allocations", "assignee="+c.Me, "workspace="+TestWorkspace, alFields)
	al := s.write("allocation-create", "POST", "/allocations",
		M{"data": M{"assignee": TestUser, "parent": c.p, "start_date": "2030-01-12", "end_date": "2030-01-14", "effort": M{"type": "hours", "value": 4}}})
	if ag := str(al, "data", "gid"); ag != "" {
		c.Made.add(&c.Made.Allocations, ag)
		s.get("allocations-one", "/allocations", "parent="+c.p, alFields)
		s.write("allocation-update", "PUT", "/allocations/"+ag, M{"data": M{"effort": M{"type": "percent", "value": 50}}})
		s.del("allocation-delete", "/allocations/"+ag)
	}

	// dependencies and search (the shapes the sync reads)
	s.write("add-dependencies", "POST", "/tasks/"+c.t2+"/addDependencies", M{"data": M{"dependencies": []string{c.t3}}})
	s.get("task-dependencies", "/tasks/"+c.t2+"/dependencies", "opt_fields=name,completed,resource_subtype")
	s.get("task-dependents", "/tasks/"+c.t3+"/dependents", "opt_fields=name,completed")
	s.get("task-with-dependencies", "/tasks/"+c.t2, "opt_fields=dependencies,dependents,dependencies.name,approval_status,resource_subtype")
	s.write("remove-dependencies", "POST", "/tasks/"+c.t2+"/removeDependencies", M{"data": M{"dependencies": []string{c.t3}}})
	s.get("search-tasks", ws+"/tasks/search", "text="+pre, "projects.any="+c.p, "opt_fields=name,completed,assignee.name,due_on")
	s.get("typeahead-tasks", ws+"/typeahead", "resource_type=task", "query="+pre, "opt_fields=name,completed")
	_, _ = c.R.CLI(c.ctx, "sync")
	c.Rep.note("shapes", fmt.Sprintf("%d 件を %s に保存", s.n, s.dir))
}
