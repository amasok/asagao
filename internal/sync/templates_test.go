package sync

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/store"
)

// templateServer is a fake Asana for templates, in the shapes of Asana's API reference: a
// job is returned by the POST (in_progress, naming what it makes) and ends after
// jobLooks GETs of /jobs/j1.
type templateServer struct {
	mu        gosync.Mutex
	status    map[string]int // "METHOD /path" → status to answer instead
	posts     []string       // "POST /path {body}"
	jobLooks  int            // GETs of /jobs/j1 until it ends; -1: never ends
	jobLooked int
	jobFailed bool
	// made: what a job made exists in Asana (GET of it works); set by the POST even
	// when the POST answers with an error (made, then the connection dropped).
	made    bool
	newName string
	// org: a workspace that is an organization (templates are listed by team only).
	org bool
}

func (f *templateServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &body)
	key := r.Method + " " + r.URL.Path
	if r.Method == http.MethodPost {
		f.posts = append(f.posts, key+" "+string(raw))
		if name, ok := body.Data["name"].(string); ok {
			f.newName = name
		}
		f.made = true
	}
	if st := f.status[key]; st != 0 {
		http.Error(w, `{"errors":[{"message":"fake `+http.StatusText(st)+`"}]}`, st)
		return
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": v}) }
	job := func() map[string]any {
		status := "in_progress"
		if f.jobLooks >= 0 && f.jobLooked >= f.jobLooks {
			status = "succeeded"
			if f.jobFailed {
				status = "failed"
			}
		}
		j := map[string]any{"gid": "j1", "resource_type": "job", "status": status,
			"new_task": nil, "new_project": nil, "new_task_template": nil}
		if strings.Contains(f.lastPost(), "instantiateTask") {
			j["resource_subtype"] = "instantiate_task"
			j["new_task"] = map[string]any{"gid": "t9", "name": f.newName, "resource_type": "task"}
		} else {
			j["resource_subtype"] = "duplicate_project"
			j["new_project"] = map[string]any{"gid": "p9", "name": f.newName, "resource_type": "project"}
		}
		return j
	}
	newTask := map[string]any{"gid": "t9", "name": f.newName, "created_at": now.Format(time.RFC3339),
		"workspace":   map[string]string{"gid": "w1"},
		"memberships": []any{map[string]any{"project": map[string]string{"gid": "p1", "name": "家"}, "section": map[string]string{"gid": "s1", "name": "未分類"}}}}
	switch key {
	case "GET /task_templates":
		if r.URL.Query().Get("project") != "p1" {
			http.Error(w, `{"errors":[{"message":"project"}]}`, 400)
			return
		}
		reply([]any{
			map[string]any{"gid": "tt1", "resource_type": "task_template", "name": "週次\x1b[2Jレビュー", "project": map[string]string{"gid": "p1"}},
			map[string]any{"gid": "tt2", "resource_type": "task_template", "name": "引っ越し準備", "project": map[string]string{"gid": "p1"}},
		})
	case "GET /project_templates":
		if f.org && r.URL.Query().Get("workspace") != "" {
			http.Error(w, `{"errors":[{"error":"requires_workspace_received_organization","message":"team required"}]}`, 400)
			return
		}
		if team := r.URL.Query().Get("team"); f.org && team != "team1" && team != "team2" {
			http.Error(w, `{"errors":[{"message":"team"}]}`, 400)
			return
		}
		reply([]any{map[string]any{"gid": "pt1", "resource_type": "project_template", "name": "新\x07居",
			"description": "説明", "public": false, "team": map[string]string{"gid": "team1", "name": "家族"},
			"requested_dates": []any{
				map[string]string{"gid": "d1", "name": "開始日", "description": "始める日"},
				map[string]string{"gid": "d2", "name": "締切", "description": ""}},
			"requested_roles": []any{map[string]string{"gid": "r1", "name": "担当"}}}})
	case "POST /task_templates/tt1/instantiateTask", "POST /project_templates/pt1/instantiateProject", "POST /projects/p1/duplicate":
		w.WriteHeader(http.StatusCreated)
		reply(job())
	case "GET /jobs/j1":
		f.jobLooked++
		reply(job())
	case "GET /tasks/t9":
		reply(newTask)
	case "GET /tasks/t9/subtasks":
		reply([]any{map[string]any{"gid": "t9a", "name": "下書き", "parent": map[string]string{"gid": "t9"}, "workspace": map[string]string{"gid": "w1"}}})
	case "GET /tasks/t9/stories", "GET /attachments":
		reply([]any{})
	case "GET /tasks":
		switch {
		case r.URL.Query().Get("project") == "p1" && f.made:
			reply([]any{newTask})
		case r.URL.Query().Get("project") == "p9":
			reply([]any{map[string]any{"gid": "t10", "name": "テンプレのタスク", "workspace": map[string]string{"gid": "w1"},
				"memberships": []any{map[string]any{"project": map[string]string{"gid": "p9"}, "section": map[string]string{"gid": "s9"}}}}})
		default:
			reply([]any{})
		}
	case "GET /projects/p9":
		reply(map[string]any{"gid": "p9", "name": f.newName, "workspace": map[string]string{"gid": "w1"},
			"team": map[string]string{"gid": "team1", "name": "家族"}, "members": []any{map[string]string{"gid": "me"}},
			"created_at": now.Format(time.RFC3339)})
	case "GET /users/me/teams":
		reply([]any{map[string]string{"gid": "team1", "name": "家族"}, map[string]string{"gid": "team2", "name": "仕事"}})
	case "GET /projects/p9/sections":
		reply([]any{map[string]string{"gid": "s9", "name": "準備"}})
	case "GET /projects":
		var ps []any
		if f.made {
			ps = append(ps, map[string]any{"gid": "p9", "name": f.newName, "workspace": map[string]string{"gid": "w1"},
				"created_at": now.Format(time.RFC3339)})
		}
		reply(ps)
	default:
		http.Error(w, `{"errors":[{"message":"not in the fake: `+key+`"}]}`, 404)
	}
}

func (f *templateServer) lastPost() string {
	if len(f.posts) == 0 {
		return ""
	}
	return f.posts[len(f.posts)-1]
}

func (f *templateServer) postsTo(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, p := range f.posts {
		if strings.Contains(strings.SplitN(p, " ", 3)[1], suffix) {
			n++
		}
	}
	return n
}

func setupTemplates(t *testing.T) (*templateServer, *Syncer, *store.Store, *int) {
	t.Helper()
	f := &templateServer{status: map[string]int{}, jobLooks: 1}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	api := asana.NewClient(srv.Client())
	api.BaseURL = srv.URL
	api.MaxRetries = 0
	waits := 0
	s := &Syncer{API: api, Store: st, now: func() time.Time { return now },
		sleep: func(ctx context.Context, d time.Duration) error { waits++; return ctx.Err() }}
	if err := st.SetSyncState(ctx, store.KeyMeGID, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertWorkspaces(ctx, []asana.Workspace{{GID: "w1", Name: "家", IsOrganization: true}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProjects(ctx, "w1", []model.Project{{GID: "p1", Name: "家", Members: []model.Ref{{GID: "me"}}}}, "me"); err != nil {
		t.Fatal(err)
	}
	return f, s, st, &waits
}

func planMark(t *testing.T, st *store.Store, feature string) string {
	t.Helper()
	v, err := st.SyncState(context.Background(), readmodel.KeyPlanUnavailable+feature)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Task templates are listed with names safe for a terminal; a 402 marks the feature
// unavailable and says so, and a list that works again clears the mark.
func TestTaskTemplatesListAndPlan(t *testing.T) {
	ctx := context.Background()
	f, s, st, _ := setupTemplates(t)
	list, err := s.TaskTemplates(ctx, "p1")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v %v", list, err)
	}
	if strings.ContainsRune(list[0].Name, 0x1b) || !strings.Contains(list[0].Name, "レビュー") {
		t.Errorf("name not made safe: %q", list[0].Name)
	}
	f.status["GET /task_templates"] = http.StatusPaymentRequired
	_, err = s.TaskTemplates(ctx, "p1")
	var pe *PlanError
	if !errors.As(err, &pe) || !model.IsPaymentRequired(err) || !strings.Contains(err.Error(), "タスクテンプレート") {
		t.Fatalf("402 err = %v", err)
	}
	if planMark(t, st, readmodel.PlanTaskTemplates) == "" {
		t.Fatal("402 did not mark the plan")
	}
	delete(f.status, "GET /task_templates")
	if _, err := s.TaskTemplates(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if v := planMark(t, st, readmodel.PlanTaskTemplates); v != "" {
		t.Errorf("mark kept after a working list: %q", v)
	}
	// Other errors leave the mark alone and are returned as they are.
	f.status["GET /task_templates"] = http.StatusInternalServerError
	if _, err := s.TaskTemplates(ctx, "p1"); err == nil || errors.As(err, &pe) {
		t.Errorf("500 err = %v", err)
	}
	if planMark(t, st, readmodel.PlanTaskTemplates) != "" {
		t.Error("500 marked the plan")
	}
}

func TestProjectTemplatesListAndPlan(t *testing.T) {
	ctx := context.Background()
	f, s, st, _ := setupTemplates(t)
	list, err := s.ProjectTemplates(ctx, "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v %v", list, err)
	}
	pt := list[0]
	if pt.Name != "新居" || len(pt.RequestedDates) != 2 || len(pt.RequestedRoles) != 1 || pt.Team == nil || pt.Team.GID != "team1" {
		t.Errorf("template = %+v", pt)
	}
	f.status["GET /project_templates"] = http.StatusPaymentRequired
	if _, err := s.ProjectTemplates(ctx, ""); !model.IsPaymentRequired(err) {
		t.Fatalf("402 err = %v", err)
	}
	if planMark(t, st, readmodel.PlanProjectTemplates) == "" {
		t.Fatal("402 did not mark the plan")
	}
	if planMark(t, st, readmodel.PlanTaskTemplates) != "" {
		t.Error("marked the other feature")
	}
}

// An organization answers 400 to a workspace: the templates of each of my teams are
// listed instead, each once.
func TestProjectTemplatesOrganization(t *testing.T) {
	ctx := context.Background()
	f, s, _, _ := setupTemplates(t)
	f.org = true
	list, err := s.ProjectTemplates(ctx, "")
	if err != nil || len(list) != 1 || list[0].GID != "pt1" {
		t.Fatalf("list = %+v %v", list, err)
	}
	if list, err := s.ProjectTemplates(ctx, "team2"); err != nil || len(list) != 1 {
		t.Fatalf("team list = %+v %v", list, err)
	}
	// Teams that cannot be listed are an error, not "no templates".
	f.status["GET /users/me/teams"] = http.StatusInternalServerError
	if _, err := s.ProjectTemplates(ctx, ""); err == nil {
		t.Error("no error when the teams cannot be listed")
	}
}

func TestTemplatesOffline(t *testing.T) {
	ctx := context.Background()
	s := &Syncer{}
	if _, err := s.TaskTemplates(ctx, "p1"); !errors.Is(err, ErrOffline) {
		t.Error(err)
	}
	if _, err := s.ProjectTemplates(ctx, ""); !errors.Is(err, ErrOffline) {
		t.Error(err)
	}
	if _, err := s.InstantiateTask(ctx, model.TaskTemplate{GID: "tt1", Name: "x"}, ""); !errors.Is(err, ErrOffline) {
		t.Error(err)
	}
	if _, err := s.InstantiateProject(ctx, model.ProjectTemplate{GID: "pt1"}, ProjectInput{Name: "x"}); !errors.Is(err, ErrOffline) {
		t.Error(err)
	}
	if _, err := s.DuplicateProject(ctx, "p1", "x", ""); !errors.Is(err, ErrOffline) {
		t.Error(err)
	}
}

var tmplTask = model.TaskTemplate{GID: "tt1", Name: "週次レビュー", Project: &model.Ref{GID: "p1"}}

// A task made from a template is named after it when no name is given, waited for, and
// stored with its subtasks.
func TestInstantiateTask(t *testing.T) {
	ctx := context.Background()
	f, s, st, _ := setupTemplates(t)
	f.jobLooks = 2
	made, err := s.InstantiateTask(ctx, tmplTask, "")
	if err != nil {
		t.Fatal(err)
	}
	if made.GID != "t9" || made.Name != "週次レビュー" || made.Pending {
		t.Errorf("made = %+v", made)
	}
	if f.jobLooked != 2 {
		t.Errorf("job looked at %d times, want until it ended (2)", f.jobLooked)
	}
	if !strings.Contains(f.posts[0], `"name":"週次レビュー"`) {
		t.Errorf("post = %s", f.posts[0])
	}
	d, err := st.GetTask(ctx, "t9")
	if err != nil || d.Name != "週次レビュー" {
		t.Fatalf("stored = %+v %v", d, err)
	}
	if sub, err := st.GetTask(ctx, "t9a"); err != nil || sub.Name != "下書き" {
		t.Errorf("subtask = %+v %v", sub, err)
	}
	// A given name is sent as it is.
	if _, err := s.InstantiateTask(ctx, tmplTask, "今週のレビュー"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.posts[1], `"name":"今週のレビュー"`) {
		t.Errorf("post = %s", f.posts[1])
	}
}

// A job still running after jobWait: the task is stored and the result says the rest is
// coming.
func TestInstantiateTaskJobStillRunning(t *testing.T) {
	ctx := context.Background()
	f, s, st, waits := setupTemplates(t)
	f.jobLooks = -1
	made, err := s.InstantiateTask(ctx, tmplTask, "")
	if err != nil {
		t.Fatal(err)
	}
	if !made.Pending || made.GID != "t9" {
		t.Errorf("made = %+v", made)
	}
	if *waits == 0 || *waits > 30 {
		t.Errorf("waited %d times", *waits)
	}
	if _, err := st.GetTask(ctx, "t9"); err != nil {
		t.Errorf("not stored: %v", err)
	}
}

// A job that failed is an error.
func TestInstantiateTaskJobFailed(t *testing.T) {
	ctx := context.Background()
	f, s, _, _ := setupTemplates(t)
	f.jobFailed = true
	if _, err := s.InstantiateTask(ctx, tmplTask, ""); err == nil || !strings.Contains(err.Error(), "失敗") {
		t.Fatalf("err = %v", err)
	}
}

// A 5xx for the POST after Asana made the task: it is found in the project, not made
// again.
func TestInstantiateTaskRecovered(t *testing.T) {
	ctx := context.Background()
	f, s, st, _ := setupTemplates(t)
	f.status["POST /task_templates/tt1/instantiateTask"] = http.StatusBadGateway
	made, err := s.InstantiateTask(ctx, tmplTask, "")
	if err != nil {
		t.Fatal(err)
	}
	if made.GID != "t9" {
		t.Errorf("made = %+v", made)
	}
	if n := f.postsTo("instantiateTask"); n != 1 {
		t.Errorf("posted %d times", n)
	}
	if _, err := st.GetTask(ctx, "t9"); err != nil {
		t.Errorf("not stored: %v", err)
	}
}

// A 5xx after which nothing is found: an error, and nothing made again.
func TestInstantiateTaskNotFoundAfter5xx(t *testing.T) {
	ctx := context.Background()
	f, s, _, waits := setupTemplates(t)
	f.status["POST /task_templates/tt1/instantiateTask"] = http.StatusBadGateway
	s.API = &notMadeAPI{AsanaAPI: s.API}
	_, err := s.InstantiateTask(ctx, tmplTask, "")
	if err == nil || !strings.Contains(err.Error(), "まだ分かりません") {
		t.Fatalf("err = %v", err)
	}
	if n := f.postsTo("instantiateTask"); n != 1 {
		t.Errorf("posted %d times", n)
	}
	if *waits != 1 {
		t.Errorf("looked again %d times, want once after projectSettle", *waits)
	}
}

// notMadeAPI lists no tasks and no projects: what the POST may have made is not there.
type notMadeAPI struct{ AsanaAPI }

func (notMadeAPI) Tasks(context.Context, model.TaskQuery) ([]model.Task, error) { return nil, nil }
func (notMadeAPI) Projects(context.Context, string, bool) ([]model.Project, error) {
	return nil, nil
}

// A 4xx, a 402 included, is not made: no looking for it, and a 402 marks the plan.
func TestInstantiateTaskRefused(t *testing.T) {
	for _, status := range []int{400, 402, 403, 404} {
		ctx := context.Background()
		f, s, st, waits := setupTemplates(t)
		f.status["POST /task_templates/tt1/instantiateTask"] = status
		if _, err := s.InstantiateTask(ctx, tmplTask, ""); err == nil {
			t.Fatalf("%d: no error", status)
		}
		if *waits != 0 {
			t.Errorf("%d: waited", status)
		}
		if marked := planMark(t, st, readmodel.PlanTaskTemplates) != ""; marked != (status == 402) {
			t.Errorf("%d: marked = %v", status, marked)
		}
	}
}

var tmplProject = model.ProjectTemplate{GID: "pt1", Name: "新居",
	RequestedDates: []model.TemplateDate{{GID: "d1", Name: "開始日"}, {GID: "d2", Name: "締切"}},
	RequestedRoles: []model.Ref{{GID: "r1", Name: "担当"}}}

// A project made from a template: the requested dates given (by name or gid) are sent,
// the others are today; the roles are sent by gid; the project is stored with its
// sections and tasks.
func TestInstantiateProject(t *testing.T) {
	ctx := context.Background()
	f, s, st, _ := setupTemplates(t)
	made, err := s.InstantiateProject(ctx, tmplProject, ProjectInput{Name: "引っ越し", TeamGID: "team1",
		Dates: map[string]string{"締切": "2026-12-24"}, Roles: map[string]string{"担当": "u2"}})
	if err != nil {
		t.Fatal(err)
	}
	if made.GID != "p9" || made.Name != "引っ越し" || made.Pending {
		t.Errorf("made = %+v", made)
	}
	var body struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal([]byte(strings.SplitN(f.posts[0], " ", 3)[2]), &body)
	got, _ := json.Marshal(body.Data)
	want := `{"name":"引っ越し","public":false,"requested_dates":[{"gid":"d1","value":"2026-11-13"},{"gid":"d2","value":"2026-12-24"}],"requested_roles":[{"gid":"r1","value":"u2"}],"team":"team1"}`
	if string(got) != want {
		t.Errorf("body = %s\nwant   %s", got, want)
	}
	ps, err := st.ListProjects(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range ps {
		if p.GID == "p9" {
			found = true
			if len(p.Sections) != 1 || p.Sections[0].Name != "準備" {
				t.Errorf("sections = %+v", p.Sections)
			}
		}
	}
	if !found {
		t.Errorf("project not stored: %+v", ps)
	}
	if d, err := st.GetTask(ctx, "t10"); err != nil || d.Name != "テンプレのタスク" {
		t.Errorf("task = %+v %v", d, err)
	}
}

func TestInstantiateProjectBadInput(t *testing.T) {
	ctx := context.Background()
	f, s, _, _ := setupTemplates(t)
	for _, in := range []ProjectInput{
		{Name: " "},
		{Name: "x", Dates: map[string]string{"開始日": "12/24"}},
		{Name: "x", Dates: map[string]string{"開始日": "2026-12-24x"}},
		{Name: "x", Dates: map[string]string{"無い日": "2026-12-24"}},
		{Name: "x", Dates: map[string]string{"": "2026-12-24"}},
		{Name: "x", Roles: map[string]string{"無い役": "u2"}},
	} {
		if _, err := s.InstantiateProject(ctx, tmplProject, in); err == nil {
			t.Errorf("%+v: no error", in)
		}
	}
	if len(f.posts) != 0 {
		t.Errorf("sent: %v", f.posts)
	}
}

// A 5xx for instantiateProject after Asana made it: found in the list, not made again.
func TestInstantiateProjectRecovered(t *testing.T) {
	ctx := context.Background()
	f, s, _, _ := setupTemplates(t)
	f.status["POST /project_templates/pt1/instantiateProject"] = http.StatusServiceUnavailable
	made, err := s.InstantiateProject(ctx, tmplProject, ProjectInput{Name: "引っ越し"})
	if err != nil {
		t.Fatal(err)
	}
	if made.GID != "p9" || !made.Pending {
		t.Errorf("made = %+v", made)
	}
	if n := f.postsTo("instantiateProject"); n != 1 {
		t.Errorf("posted %d times", n)
	}
}

func TestInstantiateProjectPlan(t *testing.T) {
	ctx := context.Background()
	f, s, st, waits := setupTemplates(t)
	f.status["POST /project_templates/pt1/instantiateProject"] = http.StatusPaymentRequired
	if _, err := s.InstantiateProject(ctx, tmplProject, ProjectInput{Name: "x"}); !model.IsPaymentRequired(err) {
		t.Fatalf("err = %v", err)
	}
	if planMark(t, st, readmodel.PlanProjectTemplates) == "" || *waits != 0 {
		t.Errorf("mark = %q waits = %d", planMark(t, st, readmodel.PlanProjectTemplates), *waits)
	}
}

// Duplicating a project sends the name, the team and what to copy, and stores the copy.
func TestDuplicateProject(t *testing.T) {
	ctx := context.Background()
	f, s, st, _ := setupTemplates(t)
	made, err := s.DuplicateProject(ctx, "p1", "家 のコピー", "team2")
	if err != nil {
		t.Fatal(err)
	}
	if made.GID != "p9" || made.Name != "家 のコピー" {
		t.Errorf("made = %+v", made)
	}
	post := f.posts[0]
	for _, want := range []string{`"name":"家 のコピー"`, `"team":"team2"`, `"include":"` + asana.ProjectDuplicateIncludes + `"`} {
		if !strings.Contains(post, want) {
			t.Errorf("post %s lacks %s", post, want)
		}
	}
	if strings.Contains(post, "forms") || strings.Contains(post, "task_projects") {
		t.Errorf("copies forms or other projects: %s", post)
	}
	if d, err := st.GetTask(ctx, "t10"); err != nil {
		t.Errorf("tasks not stored: %+v %v", d, err)
	}
	// A 402 for a duplicate is returned, and marks nothing.
	f.status["POST /projects/p1/duplicate"] = http.StatusPaymentRequired
	if _, err := s.DuplicateProject(ctx, "p1", "x", ""); !model.IsPaymentRequired(err) {
		t.Errorf("err = %v", err)
	}
	if planMark(t, st, readmodel.PlanProjectTemplates) != "" {
		t.Error("duplicate marked project templates")
	}
}

func TestPickTemplate(t *testing.T) {
	list := []model.TaskTemplate{{GID: "1", Name: "週次レビュー"}, {GID: "2", Name: "月次レビュー"}, {GID: "3", Name: "Review"}}
	pick := func(key string) (string, error) {
		t, err := PickTemplate(list, key, func(t model.TaskTemplate) string { return t.GID }, func(t model.TaskTemplate) string { return t.Name })
		return t.GID, err
	}
	for key, want := range map[string]string{"2": "2", "週次レビュー": "1", "週次": "1", "review": "3", " 月次 ": "2"} {
		if got, err := pick(key); err != nil || got != want {
			t.Errorf("%q = %q %v, want %q", key, got, err, want)
		}
	}
	for _, key := range []string{"", "レビュー", "無い"} {
		if got, err := pick(key); err == nil {
			t.Errorf("%q = %q, want an error", key, got)
		}
	}
}

// A date or role given by both its gid and its name is the same one: the same value is
// accepted, different values are refused.
func TestRequestedByGIDAndName(t *testing.T) {
	ctx := context.Background()
	f, s, _, _ := setupTemplates(t)
	if _, err := s.InstantiateProject(ctx, tmplProject, ProjectInput{Name: "x",
		Dates: map[string]string{"d1": "2026-01-01", "開始日": "2026-01-01"},
		Roles: map[string]string{"r1": "u2", "担当": "u2"}}); err != nil {
		t.Fatalf("same values: %v", err)
	}
	n := len(f.posts)
	for _, in := range []ProjectInput{
		{Name: "x", Dates: map[string]string{"d1": "2026-01-01", "開始日": "2026-01-02"}},
		{Name: "x", Roles: map[string]string{"r1": "u2", "担当": "u3"}},
	} {
		if _, err := s.InstantiateProject(ctx, tmplProject, in); err == nil || !strings.Contains(err.Error(), "違う値") {
			t.Errorf("%+v: err = %v", in, err)
		}
	}
	if len(f.posts) != n {
		t.Errorf("sent: %v", f.posts[n:])
	}
}

// A date that is not on the calendar is refused before anything is sent.
func TestRequestedDateOnTheCalendar(t *testing.T) {
	ctx := context.Background()
	f, s, _, _ := setupTemplates(t)
	for _, v := range []string{"2026-02-30", "2026-13-01", "0000-00-00", "2026-1-1", "2026-01-01\n", " 2026-01-01"} {
		if _, err := s.InstantiateProject(ctx, tmplProject, ProjectInput{Name: "x", Dates: map[string]string{"d1": v}}); err == nil {
			t.Errorf("%q accepted", v)
		}
	}
	if len(f.posts) != 0 {
		t.Errorf("sent: %v", f.posts)
	}
	if _, err := s.InstantiateProject(ctx, tmplProject, ProjectInput{Name: "x", Dates: map[string]string{"d1": "2028-02-29"}}); err != nil {
		t.Errorf("leap day: %v", err)
	}
}
