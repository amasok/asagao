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
