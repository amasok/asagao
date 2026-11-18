package asana

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana/model"
)

// The template calls of #2: their paths, parameters, bodies and answers.
func TestTemplateCalls(t *testing.T) {
	ctx := context.Background()
	bodies := map[string]string{}
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		key := r.Method + " " + r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		bodies[key] = string(raw)
		switch key {
		case "GET /task_templates":
			if q.Get("project") != "p1" || !strings.Contains(q.Get("opt_fields"), "name") {
				t.Errorf("task_templates query %v", q)
			}
			fmt.Fprint(w, `{"data":[{"gid":"tt1","name":"週次","project":{"gid":"p1"},"created_at":"2026-11-01T00:00:00.000Z"}]}`)
		case "GET /project_templates":
			if (q.Get("team") == "") == (q.Get("workspace") == "") {
				t.Errorf("project_templates needs team or workspace, not both: %v", q)
			}
			for _, f := range []string{"requested_dates.name", "requested_roles.name", "team.name"} {
				if !strings.Contains(q.Get("opt_fields"), f) {
					t.Errorf("project_templates opt_fields lack %s", f)
				}
			}
			fmt.Fprint(w, `{"data":[{"gid":"pt1","name":"新居","requested_dates":[{"gid":"d1","name":"開始日"}],"requested_roles":[{"gid":"r1","name":"担当"}]}]}`)
		case "POST /task_templates/tt1/instantiateTask", "POST /project_templates/pt1/instantiateProject", "POST /projects/p1/duplicate":
			if !strings.Contains(q.Get("opt_fields"), "new_task") || !strings.Contains(q.Get("opt_fields"), "new_project") {
				t.Errorf("job opt_fields = %s", q.Get("opt_fields"))
			}
			fmt.Fprint(w, `{"data":{"gid":"j1","resource_type":"job","status":"in_progress","new_task":{"gid":"t9"},"new_project":null}}`)
		case "GET /jobs/j1":
			fmt.Fprint(w, `{"data":{"gid":"j1","status":"succeeded","new_project":{"gid":"p9","name":"新居"}}}`)
		case "POST /task_templates/empty/instantiateTask":
			fmt.Fprint(w, `{"data":{}}`)
		default:
			t.Errorf("unexpected %s", key)
		}
	})
	if l, err := c.TaskTemplates(ctx, "p1"); err != nil || len(l) != 1 || l[0].Project.GID != "p1" || l[0].CreatedAt.IsZero() {
		t.Fatalf("task templates = %+v %v", l, err)
	}
	for _, team := range []string{"", "team1"} {
		if l, err := c.ProjectTemplates(ctx, "w1", team); err != nil || len(l) != 1 || l[0].RequestedDates[0].GID != "d1" || l[0].RequestedRoles[0].Name != "担当" {
			t.Fatalf("project templates = %+v %v", l, err)
		}
	}
	j, err := c.InstantiateTask(ctx, "tt1", "")
	if err != nil || j.GID != "j1" || j.Status != model.JobInProgress || j.NewTask.GID != "t9" || j.NewProject != nil || j.Done() {
		t.Fatalf("job = %+v %v", j, err)
	}
	if b := bodies["POST /task_templates/tt1/instantiateTask"]; b != `{"data":{}}` {
		t.Errorf("no name sent as %s", b)
	}
	if _, err := c.InstantiateProject(ctx, "pt1", ProjectFromTemplate{Name: "x", Dates: map[string]string{"d2": "2026-12-02", "d1": "2026-12-01"}}); err != nil {
		t.Fatal(err)
	}
	var body struct{ Data map[string]any }
	_ = json.Unmarshal([]byte(bodies["POST /project_templates/pt1/instantiateProject"]), &body)
	if got, _ := json.Marshal(body.Data); string(got) != `{"name":"x","public":false,"requested_dates":[{"gid":"d1","value":"2026-12-01"},{"gid":"d2","value":"2026-12-02"}]}` {
		t.Errorf("instantiateProject body = %s", got)
	}
	if _, err := c.DuplicateProject(ctx, "p1", "copy", ""); err != nil {
		t.Fatal(err)
	}
	if b := bodies["POST /projects/p1/duplicate"]; strings.Contains(b, "team") || !strings.Contains(b, `"include":"`+ProjectDuplicateIncludes) {
		t.Errorf("duplicate body = %s", b)
	}
	if j, err := c.Job(ctx, "j1"); err != nil || !j.Done() || j.NewProject.GID != "p9" {
		t.Fatalf("job = %+v %v", j, err)
	}
	if _, err := c.InstantiateTask(ctx, "empty", "x"); err == nil {
		t.Error("an empty job was accepted")
	}
}
