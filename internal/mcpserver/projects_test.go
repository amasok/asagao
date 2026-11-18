package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/store"
	"github.com/amasok/asagao/internal/sync"
)

// seedProjects is seed with p1 in a team, p2 in another, p2 a favorite, and p1's fields.
func seedProjects(t *testing.T) *store.Store {
	t.Helper()
	st := seed(t)
	ctx := context.Background()
	me := []asana.Ref{{GID: "me"}}
	if err := st.UpsertProjects(ctx, "w1", []asana.Project{
		{GID: "p1", Name: "[家族]新居プロジェクト", Members: me, Team: &asana.Ref{GID: "tm1", Name: "家族"},
			Notes: "年内", HTMLNotes: "<body><strong>年内</strong></body>", Owner: &asana.Ref{GID: "me", Name: "Me"}, DueOn: "2026-12-31"},
		{GID: "p2", Name: "仕事", Members: me, Team: &asana.Ref{GID: "tm2", Name: "会社"}},
	}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplaceFavoriteProjects(ctx, "w1", []asana.Ref{{GID: "p2"}}); err != nil {
		t.Fatal(err)
	}
	return st
}

// #38: list_projects lists my favorites first and narrows to a team.
func TestListProjectsByTeam(t *testing.T) {
	cs := connect(t, &Server{Store: seedProjects(t)})
	var out ProjectsOutput
	call(t, cs, "list_projects", nil, &out)
	if len(out.Projects) != 2 || out.Projects[0].GID != "p2" || !out.Projects[0].Favorite || out.Projects[1].TeamName != "家族" {
		t.Fatalf("projects = %+v", out.Projects)
	}
	call(t, cs, "list_projects", map[string]any{"team": "家族"}, &out)
	if len(out.Projects) != 1 || out.Projects[0].GID != "p1" {
		t.Fatalf("team 家族 = %+v", out.Projects)
	}
	if msg := toolError(call(t, cs, "list_projects", map[string]any{"team": "経理"}, nil)); msg == "" {
		t.Fatal("unknown team accepted")
	}
}

// #39: get_project has the overview from the cache offline; update_project queues the
// fields offline (no Asana needed for them).
func TestProjectFieldsOffline(t *testing.T) {
	st := seedProjects(t)
	cs := connect(t, &Server{Store: st})
	var p ProjectOutput
	call(t, cs, "get_project", map[string]any{"project": "新居"}, &p)
	if p.Markdown != "**年内**" || p.Owner == nil || p.Owner.Name != "Me" || p.DueOn != "2026-12-31" || p.Brief != nil || p.Counts != nil {
		t.Fatalf("get_project = %+v", p)
	}

	var w ProjectWriteOutput
	call(t, cs, "update_project", map[string]any{"project": "新居", "notes": "*来年*", "owner": "", "start_on": "2026-12-01",
		"color": "light-teal"}, &w)
	if w.ProjectGID != "p1" || w.Sent == nil || *w.Sent || !strings.Contains(w.Note, "next sync") {
		t.Fatalf("update_project = %+v", w)
	}
	p = ProjectOutput{} // decoded afresh: omitted fields must not keep the old values
	call(t, cs, "get_project", map[string]any{"project": "新居"}, &p)
	if p.Markdown != "*来年*" || p.Owner != nil || p.StartOn != "2026-12-01" || p.Color != "light-teal" || !p.Pending {
		t.Fatalf("after update = %+v", p)
	}
	ops, _ := st.ListOps(context.Background())
	if len(ops) != 1 || ops[0].Kind != store.OpUpdateProject {
		t.Fatalf("ops = %+v", ops)
	}
	// Archiving still needs Asana; the fields are queued all the same and it says so.
	msg := toolError(call(t, cs, "update_project", map[string]any{"project": "新居", "color": "none", "archived": true}, nil))
	if !strings.Contains(msg, "saved locally") {
		t.Fatalf("fields + archive offline: %q", msg)
	}
	// Invalid input changes nothing.
	for _, args := range []map[string]any{
		{"project": "新居", "color": "rainbow"},
		{"project": "新居", "due_on": "そのうち"},
		{"project": "新居", "owner": "誰か"},
		{"project": "仕事", "start_on": "2026-12-01"},
	} {
		if msg := toolError(call(t, cs, "update_project", args, nil)); msg == "" {
			t.Errorf("%v accepted", args)
		}
	}
	if ops, _ := st.ListOps(context.Background()); len(ops) != 1 || ops[0].Payload["color"] != "none" {
		t.Fatalf("ops after refused updates = %+v", ops)
	}
	// #52: a name Asana's GET answers is sent as its written name.
	call(t, cs, "update_project", map[string]any{"project": "新居", "color": "Cool-Gray"}, &w)
	if ops, _ := st.ListOps(context.Background()); len(ops) != 1 || ops[0].Payload["color"] != "dark-warm-gray" {
		t.Fatalf("ops after color cool-gray = %+v", ops)
	}
}

// Online, update_project sends the fields, get_project fetches the progress and the brief
// (none when Asana answers 402), and create_project takes a team.
func TestProjectFieldsOnline(t *testing.T) {
	st := seedProjects(t)
	var put, created map[string]any
	due := "2026-12-31"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Data map[string]any `json:"data"`
		}
		_ = json.Unmarshal(b, &body)
		switch r.Method + " " + r.URL.Path {
		case "GET /users/me":
			fmt.Fprint(w, `{"data":{"gid":"me","name":"Me"}}`)
		case "GET /projects/p1":
			fmt.Fprintf(w, `{"data":{"gid":"p1","name":"[家族]新居プロジェクト","notes":"年内","due_on":%q,"owner":{"gid":"me"},
				"workspace":{"gid":"w1"},"team":{"gid":"tm1","name":"家族"},"members":[{"gid":"me"}],"project_brief":{"gid":"b1"}}}`, due)
		case "PUT /projects/p1":
			put = body.Data
			due, _ = put["due_on"].(string)
			fmt.Fprintf(w, `{"data":{"gid":"p1","name":"[家族]新居プロジェクト","notes":"年内","due_on":%q,"workspace":{"gid":"w1"},"team":{"gid":"tm1","name":"家族"}}}`, due)
		case "GET /projects/p1/task_counts":
			fmt.Fprint(w, `{"data":{"num_tasks":3,"num_completed_tasks":3}}`)
		case "GET /project_briefs/b1":
			w.WriteHeader(http.StatusPaymentRequired)
			fmt.Fprint(w, `{"errors":[{"message":"upgrade"}]}`)
		case "GET /status_updates":
			fmt.Fprint(w, `{"data":[]}`)
		case "POST /projects":
			created = body.Data
			fmt.Fprint(w, `{"data":{"gid":"p9","name":"新規","workspace":{"gid":"w1"}}}`)
		case "GET /projects/p9/sections":
			fmt.Fprint(w, `{"data":[]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	}))
	defer srv.Close()
	cs := connect(t, &Server{Store: st, NewSyncer: func(context.Context) (*sync.Syncer, error) {
		api := asana.NewClient(srv.Client())
		api.BaseURL, api.MaxRetries = srv.URL, 0
		return &sync.Syncer{API: api, Store: st}, nil
	}})
	var w ProjectWriteOutput
	call(t, cs, "update_project", map[string]any{"project": "新居", "due_on": "2027-03-31"}, &w)
	if w.Sent == nil || !*w.Sent || put["due_on"] != "2027-03-31" {
		t.Fatalf("update_project = %+v, put %v", w, put)
	}
	var p ProjectOutput
	call(t, cs, "get_project", map[string]any{"project": "新居"}, &p)
	if p.Counts == nil || p.Counts.Tasks != 3 || p.Brief != nil || p.Note != "" || p.DueOn != "2027-03-31" {
		t.Fatalf("get_project = %+v", p)
	}
	// Two teams: create_project needs one.
	if msg := toolError(call(t, cs, "create_project", map[string]any{"name": "新規"}, nil)); !strings.Contains(msg, "team") {
		t.Fatalf("create_project without a team: %q", msg)
	}
	call(t, cs, "create_project", map[string]any{"name": "新規", "team": "会社"}, &w)
	if w.ProjectGID != "p9" || created["team"] != "tm2" {
		t.Fatalf("create_project = %+v, body %v", w, created)
	}
}
