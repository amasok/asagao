package asana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// The project calls of #38 and #39: their paths, parameters and answers.
func TestProjectCalls(t *testing.T) {
	ctx := context.Background()
	var put map[string]any
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.Method + " " + r.URL.Path {
		case "GET /projects/p1":
			for _, f := range []string{"html_notes", "owner.name", "start_on", "due_on", "project_brief"} {
				if !strings.Contains(q.Get("opt_fields"), f) {
					t.Errorf("project opt_fields lack %s: %s", f, q.Get("opt_fields"))
				}
			}
			fmt.Fprint(w, `{"data":{"gid":"p1","owner":{"gid":"u1","name":"花子"},"start_on":"2026-12-01","due_on":"2026-12-31",
				"html_notes":"<body>x</body>","project_brief":{"gid":"b1"}}}`)
		case "PUT /projects/p1":
			var body struct{ Data map[string]any }
			_ = json.NewDecoder(r.Body).Decode(&body)
			put = body.Data
			fmt.Fprint(w, `{"data":{"gid":"p1","color":"none"}}`)
		case "GET /projects/p1/task_counts":
			if !strings.Contains(q.Get("opt_fields"), "num_completed_tasks") {
				t.Errorf("task_counts opt_fields: %s", q.Get("opt_fields"))
			}
			fmt.Fprint(w, `{"data":{"num_tasks":5,"num_completed_tasks":2,"num_milestones":1,"num_completed_milestones":1}}`)
		case "GET /project_briefs/b1":
			fmt.Fprint(w, `{"data":{"gid":"b1","title":"t","text":"x","html_text":"<body>x</body>"}}`)
		case "GET /users/me/teams":
			if q.Get("organization") != "w1" {
				t.Errorf("teams query: %v", q)
			}
			fmt.Fprint(w, `{"data":[{"gid":"tm1","name":"営業"}]}`)
		case "GET /users/me/favorites":
			if q.Get("resource_type") != "project" || q.Get("workspace") != "w1" {
				t.Errorf("favorites query: %v", q)
			}
			fmt.Fprint(w, `{"data":[{"gid":"p1","name":"新居"}]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	p, err := c.Project(ctx, "p1")
	if err != nil || p.Owner == nil || p.Owner.Name != "花子" || p.StartOn != "2026-12-01" || p.DueOn != "2026-12-31" ||
		p.HTMLNotes != "<body>x</body>" || p.ProjectBrief == nil || p.ProjectBrief.GID != "b1" {
		t.Fatalf("Project = %+v %v", p, err)
	}
	if p, err := c.UpdateProject(ctx, "p1", map[string]any{"color": "none", "owner": nil}); err != nil || p.Color != "none" {
		t.Fatalf("UpdateProject = %+v %v", p, err)
	}
	if v, ok := put["owner"]; !ok || v != nil || put["color"] != "none" {
		t.Fatalf("PUT body = %v", put)
	}
	if n, err := c.TaskCounts(ctx, "p1"); err != nil || *n != (TaskCounts{NumTasks: 5, NumCompletedTasks: 2, NumMilestones: 1, NumCompletedMiles: 1}) {
		t.Fatalf("TaskCounts = %+v %v", n, err)
	}
	if b, err := c.ProjectBrief(ctx, "b1"); err != nil || b.Title != "t" || b.HTMLText != "<body>x</body>" {
		t.Fatalf("ProjectBrief = %+v %v", b, err)
	}
	if teams, err := c.MyTeams(ctx, "w1"); err != nil || len(teams) != 1 || teams[0].Name != "営業" {
		t.Fatalf("MyTeams = %+v %v", teams, err)
	}
	if favs, err := c.FavoriteProjects(ctx, "w1"); err != nil || len(favs) != 1 || favs[0].GID != "p1" {
		t.Fatalf("FavoriteProjects = %+v %v", favs, err)
	}
}

// A project brief on a plan without it: the 402 is reported as such.
func TestProjectBriefPaymentRequired(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		fmt.Fprint(w, `{"errors":[{"message":"premium only"}]}`)
	})
	if _, err := c.ProjectBrief(context.Background(), "b1"); !IsPaymentRequired(err) {
		t.Fatalf("err = %v", err)
	}
}
