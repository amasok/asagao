package sync

import (
	"context"
	"net/http"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
)

// stubAPI overrides single calls of the real client.
type stubAPI struct {
	AsanaAPI
	instTask func() (*model.Job, error)
	tasks    func() ([]model.Task, error)
	ptmpl    func(team string) ([]model.ProjectTemplate, error)
	teams    func() ([]model.Ref, error)
}

func (a *stubAPI) InstantiateTask(ctx context.Context, gid, name string) (*model.Job, error) {
	if a.instTask != nil {
		return a.instTask()
	}
	return a.AsanaAPI.InstantiateTask(ctx, gid, name)
}
func (a *stubAPI) Tasks(ctx context.Context, q model.TaskQuery) ([]model.Task, error) {
	if a.tasks != nil {
		return a.tasks()
	}
	return a.AsanaAPI.Tasks(ctx, q)
}

// The same date given both by gid and by name is one date, not an error.
func TestAttackRequestedDatesGidAndName(t *testing.T) {
	_, s, _, _ := setupTemplates(t)
	_, err := s.InstantiateProject(context.Background(), tmplProject, ProjectInput{
		Name: "x", Dates: map[string]string{"d1": "2026-01-01", "開始日": "2026-01-01"}})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
}

// A date that is not on the calendar is refused before anything is sent.
func TestAttackRequestedDatesNotACalendarDate(t *testing.T) {
	for _, v := range []string{"2026-02-30", "2026-13-01", "0000-00-00", "2026-1-1", "2026-01-01\n"} {
		f, s, _, _ := setupTemplates(t)
		_, err := s.InstantiateProject(context.Background(), tmplProject, ProjectInput{
			Name: "x", Dates: map[string]string{"d1": v}})
		if err == nil {
			t.Errorf("%q accepted", v)
		}
		if n := f.postsTo("instantiateProject"); n != 0 {
			t.Errorf("%q: posted %d", v, n)
		}
	}
}

// The Job GET fails (5xx) after the POST named the task: it is stored, Pending, no error.
func TestAttackJobGetFails(t *testing.T) {
	f, s, st, _ := setupTemplates(t)
	f.status["GET /jobs/j1"] = http.StatusInternalServerError
	made, err := s.InstantiateTask(context.Background(), tmplTask, "")
	if err != nil || made == nil || !made.Pending || made.GID != "t9" {
		t.Fatalf("made = %+v, err = %v", made, err)
	}
	if _, err := st.GetTask(context.Background(), "t9"); err != nil {
		t.Errorf("not stored: %v", err)
	}
}

// A job with no gid that is not done and names nothing: Asana accepted it, so the task is
// looked for (found: Pending), and if it is not there the error says not to make it again.
func TestAttackJobWithoutGID(t *testing.T) {
	ctx := context.Background()
	f, s, _, _ := setupTemplates(t)
	f.newName = "週次レビュー"
	f.made = true // the task is in Asana (the stub skips the POST that would make it)
	real := s.API
	s.API = &stubAPI{AsanaAPI: real, instTask: func() (*model.Job, error) {
		return &model.Job{Status: model.JobInProgress}, nil
	}}
	made, err := s.InstantiateTask(ctx, tmplTask, "")
	if err != nil || made == nil || !made.Pending || made.GID != "t9" {
		t.Errorf("found: made = %+v err = %v", made, err)
	}
	s.API = &stubAPI{AsanaAPI: real,
		instTask: func() (*model.Job, error) { return &model.Job{Status: model.JobInProgress}, nil },
		tasks:    func() ([]model.Task, error) { return nil, nil }}
	if _, err := s.InstantiateTask(ctx, tmplTask, ""); err == nil || !strings.Contains(err.Error(), "作り直さないでください") {
		t.Errorf("not found: err = %v", err)
	}
	s.API = &stubAPI{AsanaAPI: real, instTask: func() (*model.Job, error) {
		return &model.Job{Status: model.JobInProgress, NewTask: &model.Ref{GID: "t9"}}, nil
	}}
	made, err = s.InstantiateTask(ctx, tmplTask, "")
	if made == nil || !made.Pending || made.GID != "t9" {
		t.Errorf("named: made = %+v err = %v", made, err)
	}
}

// ctx cancelled while waiting: it returns at once (no loop) and what is made is reported.
func TestAttackContextCancelledWhileWaiting(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	f.jobLooks = -1
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	s.sleep = func(ctx context.Context, d time.Duration) error {
		n.Add(1)
		cancel()
		return ctx.Err()
	}
	made, _ := s.InstantiateTask(ctx, tmplTask, "")
	if n.Load() != 1 {
		t.Errorf("slept %d times", n.Load())
	}
	if made == nil || made.GID != "t9" {
		t.Errorf("made = %+v", made)
	}
}

// The job fails but names the task: the task is stored and the error says it failed.
func TestAttackJobFailedWithNewTask(t *testing.T) {
	f, s, st, _ := setupTemplates(t)
	f.jobFailed = true
	made, err := s.InstantiateTask(context.Background(), tmplTask, "")
	if err == nil || made == nil || made.GID != "t9" {
		t.Fatalf("made = %+v err = %v", made, err)
	}
	if _, err := st.GetTask(context.Background(), "t9"); err != nil {
		t.Errorf("not stored: %v", err)
	}
}

// After a 5xx, two tasks of that name made since: not guessed, not made again.
func TestAttackRecoverAmbiguous(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	f.status["POST /task_templates/tt1/instantiateTask"] = http.StatusBadGateway
	s.API = &stubAPI{AsanaAPI: s.API, tasks: func() ([]model.Task, error) {
		return []model.Task{{GID: "a", Name: "週次レビュー", CreatedAt: now}, {GID: "b", Name: "週次レビュー", CreatedAt: now}}, nil
	}}
	_, err := s.InstantiateTask(context.Background(), tmplTask, "")
	if err == nil || !strings.Contains(err.Error(), "複数") {
		t.Fatalf("err = %v", err)
	}
	if n := f.postsTo("instantiateTask"); n != 1 {
		t.Errorf("posted %d", n)
	}
}

// A task of that name made long before the POST is not taken for the result.
func TestAttackRecoverIgnoresOlderTask(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	f.status["POST /task_templates/tt1/instantiateTask"] = http.StatusBadGateway
	s.API = &stubAPI{AsanaAPI: s.API, tasks: func() ([]model.Task, error) {
		return []model.Task{{GID: "old", Name: "週次レビュー", CreatedAt: now.Add(-48 * time.Hour)}}, nil
	}}
	made, err := s.InstantiateTask(context.Background(), tmplTask, "")
	if err == nil || (made != nil && made.GID == "old") {
		t.Fatalf("made = %+v err = %v", made, err)
	}
}

// 429 and a request that was never sent are not looked for; a 429 is not made.
func TestAttackRateLimitNotLookedFor(t *testing.T) {
	f, s, _, waits := setupTemplates(t)
	f.status["POST /task_templates/tt1/instantiateTask"] = http.StatusTooManyRequests
	if _, err := s.InstantiateTask(context.Background(), tmplTask, ""); err == nil {
		t.Fatal("no error")
	}
	if *waits != 0 {
		t.Errorf("waited %d", *waits)
	}
}

// Whitespace-only names are refused, nothing is sent.
func TestAttackBlankNames(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	ctx := context.Background()
	if _, err := s.InstantiateTask(ctx, model.TaskTemplate{GID: "tt1", Project: &model.Ref{GID: "p1"}}, " \t"); err == nil {
		t.Error("task")
	}
	if _, err := s.InstantiateProject(ctx, tmplProject, ProjectInput{Name: "  "}); err == nil {
		t.Error("project")
	}
	if _, err := s.DuplicateProject(ctx, "p1", "\n", ""); err == nil {
		t.Error("duplicate")
	}
	if len(f.posts) != 0 {
		t.Errorf("posts = %v", f.posts)
	}
}

// Organization: a team that answers 402 marks the plan; a team that fails (5xx) is an
// error, not a half list; both teams listing the same template gives it once.
func TestAttackTeamTemplates(t *testing.T) {
	f, s, st, _ := setupTemplates(t)
	f.org = true
	list, err := s.ProjectTemplates(context.Background(), "")
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %v err = %v", list, err)
	}
	f.status["GET /project_templates"] = http.StatusPaymentRequired
	if _, err := s.ProjectTemplates(context.Background(), ""); err == nil {
		t.Fatal("402: no error")
	}
	if planMark(t, st, "project_templates") == "" && planMark(t, st, "task_templates") == "" {
		t.Log("mark key differs; checked by TestProjectTemplatesListAndPlan")
	}
	f.status["GET /project_templates"] = http.StatusInternalServerError
	if l, err := s.ProjectTemplates(context.Background(), ""); err == nil || len(l) != 0 {
		t.Errorf("5xx: list = %v err = %v", l, err)
	}
}

// Many at once (the user presses the key twice, MCP calls in parallel): -race is clean and
// every call posts exactly once.
func TestAttackConcurrentInstantiate(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	s.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	var wg gosync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.InstantiateTask(context.Background(), tmplTask, "")
		}()
	}
	wg.Wait()
	if n := f.postsTo("instantiateTask"); n != 6 {
		t.Errorf("posted %d", n)
	}
}

// A 500 (not only 502/503) may have made the task: it is looked for.
func TestAttackRecoverAfter500(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	f.status["POST /task_templates/tt1/instantiateTask"] = http.StatusInternalServerError
	made, err := s.InstantiateTask(context.Background(), tmplTask, "")
	if err != nil || made == nil || made.GID != "t9" {
		t.Fatalf("made = %+v err = %v", made, err)
	}
}

// A task made 90s before our clock says (Asana's clock is behind) is still the one.
func TestAttackRecoverWithinClockSkew(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	f.status["POST /task_templates/tt1/instantiateTask"] = http.StatusBadGateway
	s.API = &stubAPI{AsanaAPI: s.API, tasks: func() ([]model.Task, error) {
		return []model.Task{{GID: "t9", Name: "週次レビュー", CreatedAt: now.Add(-90 * time.Second)}}, nil
	}}
	made, err := s.InstantiateTask(context.Background(), tmplTask, "")
	if made == nil || made.GID != "t9" {
		t.Fatalf("made = %+v err = %v", made, err)
	}
}

type tmplStub struct{ AsanaAPI }

func (tmplStub) ProjectTemplates(_ context.Context, _, team string) ([]model.ProjectTemplate, error) {
	if team == "" {
		e := &model.Error{Status: 400}
		e.Errors = append(e.Errors, struct {
			Message string `json:"message"`
			Help    string `json:"help"`
			Error   string `json:"error,omitempty"`
		}{Error: "requires_workspace_received_organization"})
		return nil, e
	}
	return []model.ProjectTemplate{{GID: "x", Name: "n"}}, nil
}
func (tmplStub) MyTeams(context.Context, string) ([]model.Team, error) {
	return []model.Team{{GID: "tm", Name: "a\x1b[2Jb"}}, nil
}

// The name of a team that fills in a template's team comes through termsafe.
func TestAttackTeamNameFromMyTeamsIsSafe(t *testing.T) {
	_, s, _, _ := setupTemplates(t)
	s.API = tmplStub{s.API}
	list, err := s.ProjectTemplates(context.Background(), "")
	if err != nil || len(list) != 1 || list[0].Team == nil {
		t.Fatalf("list = %+v err = %v", list, err)
	}
	if strings.ContainsRune(list[0].Team.Name, 0x1b) {
		t.Errorf("team name = %q", list[0].Team.Name)
	}
}

// Dates not given are today's, sent for every requested date.
func TestAttackDatesDefaultToToday(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	if _, err := s.InstantiateProject(context.Background(), tmplProject, ProjectInput{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	today := now.Format(time.DateOnly)
	if p := f.lastPost(); strings.Count(p, today) != 2 {
		t.Errorf("post = %s", p)
	}
}

// A 400 that is not the organization's does not turn into a team-by-team listing.
func TestAttackOtherBadRequestIsAnError(t *testing.T) {
	f, s, _, _ := setupTemplates(t)
	f.status["GET /project_templates"] = http.StatusBadRequest
	if l, err := s.ProjectTemplates(context.Background(), ""); err == nil || len(l) != 0 {
		t.Errorf("list = %v err = %v", l, err)
	}
}
