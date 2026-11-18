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
