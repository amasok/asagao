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
	if err != nil { // the status is in asagao's error ("asana: 400 …")
		text = fmt.Sprintf("// %s\n// error: %s\n%s\n", req, oneLine(err.Error()), body)
	}
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
	if err == nil {
		s.c.Made.add(&s.c.Made.Deleted, strings.Trim(path, "/"))
	}
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
	multi := s.write("custom-field-multi-create", "POST", "/custom_fields",
		M{"data": M{"workspace": TestWorkspace, "name": pre + "-multi", "resource_subtype": "multi_enum",
			"enum_options": []M{{"name": pre + "-x"}, {"name": pre + "-y"}}}})
	if mg := str(multi, "data", "gid"); mg != "" {
		c.Made.add(&c.Made.CustomFields, mg)
		s.write("multi-addCustomFieldSetting", "POST", "/projects/"+c.p+"/addCustomFieldSetting", M{"data": M{"custom_field": mg}})
		opt := ""
		if a := asArray(get(multi, "data", "enum_options")); len(a) > 0 {
			opt = str(a[0], "gid")
		}
		for i, v := range []any{[]string{opt}, nil, []string{}} {
			s.write(fmt.Sprintf("put-task-custom-field-multi_enum-%d", i), "PUT", "/tasks/"+c.t1, M{"data": M{"custom_fields": M{mg: v}}})
		}
		s.write("multi-removeCustomFieldSetting", "POST", "/projects/"+c.p+"/removeCustomFieldSetting", M{"data": M{"custom_field": mg}})
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
	periods := s.get("time-periods", "/time_periods", "workspace="+TestWorkspace, "opt_fields=display_name,start_on,end_on,period,parent")
	quarter := currentQuarter(periods, time.Now().Format("2006-01-02"))
	s.write("goal-create-no-period", "POST", "/goals", M{"data": M{"name": pre + "-goal-x", "workspace": TestWorkspace, "team": c.team}})
	goal := s.write("goal-create", "POST", "/goals", M{"data": M{"name": pre + "-goal", "workspace": TestWorkspace, "team": c.team,
		"owner": c.Me, "time_period": quarter, "notes": "dummy goal"}})
	if g := str(goal, "data", "gid"); g == "" { // a goal without a period in a failed POST is not made
		c.Rep.note("ゴールを作る", "作れなかった (time_period "+quarter+")")
	}
	if gg := str(goal, "data", "gid"); gg != "" {
		c.Made.add(&c.Made.Goals, gg)
		s.get("goal", "/goals/"+gg, goalFields)
		// the default metric is a percentage 0 → 1: set it to 40% and read it back
		s.write("goal-set-current-percentage", "POST", "/goals/"+gg+"/setMetricCurrentValue", M{"data": M{"current_number_value": 0.4}})
		s.get("goal-percentage-40", "/goals/"+gg, goalFields)
		sub := s.write("subgoal-create", "POST", "/goals", M{"data": M{"name": pre + "-subgoal", "workspace": TestWorkspace, "team": c.team,
			"owner": c.Me, "time_period": quarter}})
		if sg := str(sub, "data", "gid"); sg != "" {
			c.Made.add(&c.Made.Goals, sg)
			s.write("goal-add-subgoal", "POST", "/goals/"+gg+"/addSupportingRelationship", M{"data": M{"supporting_resource": sg}})
			s.write("goal-add-project", "POST", "/goals/"+gg+"/addSupportingRelationship", M{"data": M{"supporting_resource": c.p}})
			s.get("goal-relationships", "/goal_relationships", "supported_goal="+gg,
				"opt_fields=supporting_resource.name,supporting_resource.resource_type,supported_goal.name,contribution_weight,resource_subtype")
			s.get("goal-with-relationships", "/goals/"+gg, goalFields+",goal_relationships")
			s.write("goal-remove-subgoal", "POST", "/goals/"+gg+"/removeSupportingRelationship", M{"data": M{"supporting_resource": sg}})
			s.del("subgoal-delete", "/goals/"+sg)
		}
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
		inner := s.write("portfolio-nested-create", "POST", "/portfolios", M{"data": M{"name": pre + "-portfolio-inner", "workspace": TestWorkspace, "public": false}})
		if ig := str(inner, "data", "gid"); ig != "" {
			c.Made.add(&c.Made.Portfolios, ig)
			s.write("portfolio-add-nested", "POST", "/portfolios/"+pg+"/addItem", M{"data": M{"item": ig}})
			s.get("portfolio-items-nested", "/portfolios/"+pg+"/items", "opt_fields=name,resource_type,current_status_update.status_type,due_on,owner.name")
			s.write("portfolio-remove-nested", "POST", "/portfolios/"+pg+"/removeItem", M{"data": M{"item": ig}})
			s.del("portfolio-nested-delete", "/portfolios/"+ig)
		}
		s.get("portfolio", "/portfolios/"+pg, "opt_fields=name,owner.name,color,current_status_update.title,current_status_update.status_type,due_on,start_on,permalink_url,public,members.name,workspace.gid")
		s.get("portfolio-items", "/portfolios/"+pg+"/items", "opt_fields=name,resource_type,current_status_update.status_type,due_on,owner.name")
		s.get("portfolios-one", "/portfolios", "workspace="+TestWorkspace, "owner="+c.Me, "opt_fields=name,owner.name,color,current_status_update.title,current_status_update.status_type,due_on,start_on,permalink_url")
		s.write("portfolio-remove-item", "POST", "/portfolios/"+pg+"/removeItem", M{"data": M{"item": c.p}})
		s.del("portfolio-delete", "/portfolios/"+pg)
	}
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

// currentQuarter is the gid of the quarter (period "Q1".."Q4") that contains today.
func currentQuarter(periods any, today string) string {
	for _, p := range asArray(get(periods, "data")) {
		if strings.HasPrefix(str(p, "period"), "Q") && str(p, "start_on") <= today && today <= str(p, "end_on") {
			return str(p, "gid")
		}
	}
	return ""
}

// legacyColors are the color names asagao offered before #31 (internal/domain/tag,
// project). light-brown stays on purpose: the probe records that Asana refuses it (400),
// which is why asagao now offers light-yellow.
var legacyColors = []string{
	"dark-pink", "dark-green", "dark-blue", "dark-red", "dark-teal", "dark-brown", "dark-orange", "dark-purple", "dark-warm-gray",
	"light-pink", "light-green", "light-blue", "light-red", "light-teal", "light-brown", "light-orange", "light-purple", "light-warm-gray",
	"none",
	// not offered by asagao: light-yellow (in Asana's 400 list) and the new names GET answers
	"light-yellow", "hot-pink", "red", "aqua", "cool-gray", "yellow-green",
}

// colorNames records, for every color name asagao sends, what Asana answers to the PUT and
// to a later GET, for a tag and for a project (Asana renames the old names: dark-red comes
// back as red). Only a tag and a project made by this run are changed.
func (c *Check) colorNames() {
	c.group("色の名前 (-colors)")
	out := c.tool("create_project", "create_project", M{"name": c.Made.Prefix + "-colors", "team": c.team})
	c.p = str(out, "project_gid")
	if c.p == "" {
		c.must("create_project の gid", fmt.Errorf("%v", out))
	}
	c.Made.add(&c.Made.Projects, c.p)
	tg, err := c.R.WriteBody(c.ctx, c.Made, "POST", "/tags", M{"data": M{"workspace": TestWorkspace, "name": c.Made.Prefix + "-colors"}})
	g := str(decode(tg), "data", "gid")
	if err != nil || g == "" {
		c.must("タグを作る", fmt.Errorf("%v %s", err, tg))
	}
	c.Made.add(&c.Made.Tags, g)
	rows := []string{"| 送った色 | タグ: PUT の答え | タグ: GET | プロジェクト: PUT の答え | プロジェクト: GET |", "|---|---|---|---|---|"}
	for _, col := range legacyColors {
		c.gate()
		cell := func(kind, gid string) (string, string) {
			body, err := c.R.WriteBody(c.ctx, c.Made, "PUT", "/"+kind+"/"+gid, M{"data": M{"color": col}})
			put := str(decode(body), "data", "color")
			if err != nil {
				put = "エラー (全文は colors-errors.txt)"
				full := fmt.Sprintf("%s %s %s: %s\n", kind, gid, col, err)
				if f, ferr := os.OpenFile(filepath.Join(c.R.Env.Run, "colors-errors.txt"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); ferr == nil {
					_, _ = f.WriteString(full)
					f.Close()
				}
			}
			got, gerr := c.asana("/"+kind+"/"+gid, "opt_fields=color")
			return put, str(got, "color") + errText(gerr)
		}
		tp, tgt := cell("tags", g)
		pp, pgt := cell("projects", c.p)
		rows = append(rows, fmt.Sprintf("| %s | %s | %s | %s | %s |", col, tp, tgt, pp, pgt))
	}
	c.Rep.lines = append(c.Rep.lines, rows...)
}
