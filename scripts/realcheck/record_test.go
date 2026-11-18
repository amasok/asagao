package main

import (
	"context"
	"testing"
)

func TestGuardWrite(t *testing.T) {
	c := &Created{Prefix: "asagao-realcheck-1", Projects: []string{"1000001"}, Tasks: []string{"2000002"},
		Tags: []string{"3000003"}, Statuses: []string{"4000004"}, CustomFields: []string{"5000005"}}
	allowed := []struct {
		m, p   string
		fields []string
	}{
		{"DELETE", "/tasks/2000002", nil},
		{"DELETE", "/projects/1000001", nil},
		{"DELETE", "/tags/3000003", nil},
		{"DELETE", "/status_updates/4000004", nil},
		{"DELETE", "/custom_fields/5000005", nil},
		{"POST", "/projects/1000001/addCustomFieldSetting", []string{"custom_field=5000005"}},
		{"POST", "/custom_fields", []string{"workspace=" + TestWorkspace, "name=asagao-realcheck-1-date"}},
	}
	for _, a := range allowed {
		if err := c.guardWrite(a.m, a.p, a.fields); err != nil {
			t.Errorf("%s %s refused: %v", a.m, a.p, err)
		}
	}
	refused := []struct {
		m, p   string
		fields []string
	}{
		{"DELETE", "/tasks/9999999", nil},            // not ours
		{"DELETE", "/projects/2000002", nil},         // a task gid used as a project
		{"DELETE", "/tasks/2000002/../9999999", nil}, // path tricks
		{"DELETE", "/users/2000002", nil},            // unknown kind
		{"PUT", "/tasks/2000002", nil},               // edits go through asagao, not api
		{"POST", "/projects/9999999/addCustomFieldSetting", nil},
		{"POST", "/custom_fields", []string{"workspace=1", "name=asagao-realcheck-1-x"}},
		{"POST", "/custom_fields", []string{"workspace=" + TestWorkspace, "name=Priority"}},
		{"POST", "/custom_fields", []string{"workspace=" + TestWorkspace, "name=x asagao-realcheck-1"}},
		{"DELETE", "/workspaces/" + TestWorkspace, nil},
	}
	for _, r := range refused {
		if err := c.guardWrite(r.m, r.p, r.fields); err == nil {
			t.Errorf("%s %s %v allowed", r.m, r.p, r.fields)
		}
	}
}

// WriteAPI refuses before running anything: the binary does not exist here.
func TestWriteAPIRefusesWithoutRunning(t *testing.T) {
	r := &Runner{Env: Env{Bin: "/nonexistent/asagao"}}
	c := &Created{Prefix: "asagao-realcheck-1", Tasks: []string{"2000002"}}
	if _, err := r.WriteAPI(context.Background(), c, "DELETE", "/tasks/9999999"); err == nil || err.Error() == "" {
		t.Fatal("not refused")
	}
	if _, err := r.WriteAPI(context.Background(), c, "DELETE", "/tasks/2000002?x=1"); err == nil {
		t.Fatal("query on a write allowed")
	}
}

func TestToDiscard(t *testing.T) {
	mine := []string{"2000002"}
	before := []M{{"id": 1.0, "task_gid": "2000002"}, {"id": 2.0, "task_gid": "9999999"}}
	after := []M{
		{"id": 1.0, "task_gid": "2000002"}, // there before the step
		{"id": 2.0, "task_gid": "9999999"}, // someone else's, there before
		{"id": 3.0, "task_gid": "2000002"}, // new, ours: discard
		{"id": 4.0, "task_gid": "9999999"}, // new but not our task
		{"id": 5.0, "task_gid": ""},        // new, not about a task (tag, section, ...)
	}
	got := toDiscard(before, after, mine)
	if len(got) != 1 || outboxID(got[0]) != "3" {
		t.Fatalf("got %v", got)
	}
}

func TestGuardBody(t *testing.T) {
	pre := "asagao-realcheck-1"
	c := &Created{Prefix: pre, Projects: []string{"1000001"}, Tasks: []string{"2000002"}, Tags: []string{"3000003"},
		CustomFields: []string{"5000005"}, TimeEntries: []string{"6000006"}, Allocations: []string{"7000007"},
		ProjectTemplates: []string{"8000008"}, Goals: []string{"9100001"}, Portfolios: []string{"9200002"}}
	named := func(n string, kv ...any) M {
		d := M{"name": n}
		for i := 0; i+1 < len(kv); i += 2 {
			d[kv[i].(string)] = kv[i+1]
		}
		return M{"data": d}
	}
	allowed := []struct {
		m, p string
		b    M
	}{
		{"PUT", "/tasks/2000002", M{"data": M{"custom_fields": M{"5000005": 1}}}},
		{"POST", "/tasks/2000002/time_tracking_entries", M{"data": M{"duration_minutes": 15}}},
		{"POST", "/tasks/2000002/addDependencies", M{"data": M{"dependencies": []string{"2000002"}}}},
		{"PUT", "/time_tracking_entries/6000006", M{"data": M{"duration_minutes": 30}}},
		{"POST", "/allocations", M{"data": M{"parent": "1000001", "assignee": TestUser}}},
		{"PUT", "/allocations/7000007", M{"data": M{}}},
		{"POST", "/custom_fields/5000005/enum_options", named(pre + "-b")},
		{"POST", "/custom_fields", named(pre+"-enum", "workspace", TestWorkspace)},
		{"POST", "/projects/1000001/addCustomFieldSetting", M{"data": M{"custom_field": "5000005"}}},
		{"POST", "/projects/1000001/saveAsTemplate", named(pre+"-t", "public", false)},
		{"POST", "/projects/1000001/duplicate", named(pre + "-d")},
		{"POST", "/project_templates/8000008/instantiateProject", named(pre + "-p")},
		{"POST", "/goals", named(pre+"-g", "workspace", TestWorkspace, "team", TestTeam)},
		{"POST", "/goals/9100001/setMetric", M{"data": M{"metric_type": "number"}}},
		{"POST", "/goals/9100001/setMetricCurrentValue", M{"data": M{"current_number_value": 0.4}}},
		{"POST", "/portfolios", named(pre+"-pf", "workspace", TestWorkspace, "public", false)},
		{"POST", "/portfolios/9200002/addItem", M{"data": M{"item": "1000001"}}},
		{"POST", "/portfolios/9200002/addItem", M{"data": M{"item": "9200002"}}}, // a nested one of ours
		{"POST", "/goals/9100001/addSupportingRelationship", M{"data": M{"supporting_resource": "9100001"}}},
		{"POST", "/goals/9100001/addSupportingRelationship", M{"data": M{"supporting_resource": "1000001"}}},
		{"POST", "/tags", named(pre+"-colors", "workspace", TestWorkspace)},
		{"POST", "/tasks", named(pre+"-remote", "projects", []string{"1000001"})},
		{"PUT", "/tags/3000003", M{"data": M{"color": "dark-red"}}},
		{"PUT", "/projects/1000001", M{"data": M{"color": "none"}}},
	}
	for _, a := range allowed {
		if err := c.guardBody(a.m, a.p, a.b); err != nil {
			t.Errorf("%s %s refused: %v", a.m, a.p, err)
		}
	}
	refused := []struct {
		m, p string
		b    M
	}{
		{"PUT", "/tasks/9999999", M{"data": M{}}},
		{"PUT", "/projects/1000001", M{"data": M{}}}, // project edits go through asagao
		{"PUT", "/projects/1000001", M{"data": M{"color": "red", "name": "x"}}},
		{"PUT", "/projects/9999999", M{"data": M{"color": "red"}}},
		{"PUT", "/tags/9999999", M{"data": M{"color": "red"}}},
		{"POST", "/tags", named("urgent", "workspace", TestWorkspace)},
		{"POST", "/tasks", named(pre+"-remote", "projects", []string{"9999999"})},
		{"POST", "/tasks", named(pre+"-remote", "projects", []string{"1000001", "9999999"})},
		{"POST", "/tasks", named(pre + "-remote")}, // no project: lands in My Tasks
		{"POST", "/tasks", named("buy milk", "projects", []string{"1000001"})},
		{"POST", "/tasks/9999999/time_tracking_entries", M{"data": M{}}},
		{"PUT", "/time_tracking_entries/9999999", M{"data": M{}}},
		{"POST", "/allocations", M{"data": M{"parent": "9999999", "assignee": TestUser}}},
		{"POST", "/allocations", M{"data": M{"parent": "1000001", "assignee": "42"}}},
		{"POST", "/custom_fields/9999999/enum_options", named(pre + "-b")},
		{"POST", "/custom_fields/5000005/enum_options", named("High")},
		{"POST", "/custom_fields", named(pre+"-x", "workspace", "1")},
		{"POST", "/custom_fields", named("Priority", "workspace", TestWorkspace)},
		{"POST", "/projects/1000001/saveAsTemplate", named(pre+"-t", "public", true)},
		{"POST", "/projects/9999999/duplicate", named(pre + "-d")},
		{"POST", "/projects/1000001/duplicate", named("copy")},
		{"POST", "/project_templates/9999999/instantiateProject", named(pre + "-p")},
		{"POST", "/task_templates/1/instantiateTask", named(pre + "-x")},
		{"DELETE", "/tasks/2000002", nil},                               // deletes go through guardWrite
		{"POST", "/goals", named(pre+"-g", "workspace", TestWorkspace)}, // workspace-level goal
		{"POST", "/goals", named("Revenue", "workspace", TestWorkspace, "team", TestTeam)},
		{"POST", "/goals/9999999/setMetric", M{"data": M{}}},
		{"POST", "/goals/9999999/setMetricCurrentValue", M{"data": M{}}},
		{"POST", "/portfolios", named(pre+"-pf", "workspace", TestWorkspace)},    // public not false
		{"POST", "/portfolios/9200002/addItem", M{"data": M{"item": "9999999"}}}, // not our project
		{"POST", "/portfolios/9999999/addItem", M{"data": M{"item": "1000001"}}},
		{"POST", "/goals/9100001/addSupportingRelationship", M{"data": M{"supporting_resource": "9999999"}}}, // someone's goal
		{"POST", "/goals/9999999/addSupportingRelationship", M{"data": M{"supporting_resource": "9100001"}}},
	}
	for _, r := range refused {
		if err := c.guardBody(r.m, r.p, r.b); err == nil {
			t.Errorf("%s %s %v allowed", r.m, r.p, r.b)
		}
	}
}

func TestCollectAddsPrefixedProjectsFromTypeahead(t *testing.T) {
	c := &Created{Prefix: "asagao-realcheck-1", Projects: []string{"1000001"}}
	data := []byte(`[{"gid":"1000002","name":"asagao-realcheck-1-duplicate"},
		{"gid":"1000003","name":"test さんの最初のプロジェクト"},
		{"gid":"1000004","name":"x asagao-realcheck-1"},
		{"gid":"../1","name":"asagao-realcheck-1-evil"},
		{"gid":"1000001","name":"asagao-realcheck-1"}]`)
	c.addPrefixed(&c.Projects, data)
	if got := c.Projects; len(got) != 2 || got[1] != "1000002" {
		t.Fatalf("projects %v", got)
	}
}

func TestRecordJob(t *testing.T) {
	c := &Created{}
	c.recordJob(M{"data": M{"gid": "1", "status": "in_progress", "new_project": M{"gid": "1000002"}}})
	c.recordJob(M{"data": M{"new_project_template": M{"gid": "8000008"}, "new_task": M{"gid": "2000002"}}})
	c.recordJob(M{"data": M{"new_project": M{"gid": "not-a-gid"}}})
	if len(c.Projects) != 1 || c.Projects[0] != "1000002" || len(c.ProjectTemplates) != 1 || len(c.Tasks) != 1 {
		t.Fatalf("%+v", c)
	}
}

func TestGidsOfNothing(t *testing.T) {
	for _, v := range []any{nil, []any{}, M{}, "x"} {
		if g := gids(v); g != nil {
			t.Errorf("gids(%v) = %v", v, g)
		}
	}
	if g := gids([]any{M{"gid": "1"}, M{"gid": "2"}}); len(g) != 2 || g[1] != "2" {
		t.Errorf("gids = %v", g)
	}
}

func TestCurrentQuarter(t *testing.T) {
	periods := M{"data": []any{
		M{"gid": "y", "period": "FY", "start_on": "2026-01-01", "end_on": "2026-12-31"},
		M{"gid": "h", "period": "H2", "start_on": "2026-07-01", "end_on": "2026-12-31"},
		M{"gid": "q3", "period": "Q3", "start_on": "2026-07-01", "end_on": "2026-09-30"},
		M{"gid": "q4", "period": "Q4", "start_on": "2026-10-01", "end_on": "2026-12-31"},
	}}
	if g := currentQuarter(periods, "2026-11-18"); g != "q4" {
		t.Fatalf("got %q", g)
	}
	if g := currentQuarter(periods, "2027-01-01"); g != "" {
		t.Fatalf("got %q", g)
	}
}

func TestCanonColor(t *testing.T) {
	for in, want := range map[string]string{"dark-red": "red", "red": "red", "none": "", "": "", " Light-Blue ": "blue"} {
		if got := canonColor(in); got != want {
			t.Errorf("canonColor(%q) = %q, want %q", in, got, want)
		}
	}
}
