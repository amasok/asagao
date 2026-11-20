package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/domain/plan"
	"github.com/amasok/asagao/internal/termsafe"
)

// Templates (#2): making a task or a project from a template, and duplicating a project.
//
// Like creating a project, these need Asana right away and are not queued in the outbox:
// Asana makes the result in an asynchronous job (with the template's subtasks, sections,
// tasks and dates), which a local placeholder could not stand in for, and the user wants
// to work in the new project or task right after. A failure that may have made it anyway
// (a 5xx, a dropped connection) is checked against Asana, as for CreateProject; nothing
// is made twice.
//
// The job is waited for (GET /jobs/{gid}) up to jobWait. If it is still running, what is
// there is stored and the result says so (Pending): the rest arrives with the next sync.
//
// A 402 is recorded for the workspace (store.RecordPlanRefusal, sync_state.go), so the TUI stops
// offering the feature there for plan.RecheckAfter; a list that works again
// forgets it.

// Made is what a template or a duplicate made.
type Made struct {
	GID  string `json:"gid"`
	Name string `json:"name"`
	// Pending: Asana was still making it (subtasks, tasks) when asagao stopped waiting.
	// The rest is fetched by the next sync.
	Pending bool `json:"pending,omitempty"`
}

// ProjectInput is what a project made from a template is given.
type ProjectInput struct {
	Name    string
	TeamGID string // "" for the template's team
	Public  bool
	// Dates are values for the template's requested dates, keyed by the date's gid or
	// name; a date not given is today.
	Dates map[string]string
	// Roles are users (gids) for the template's requested roles, keyed by the role's gid
	// or name; a role not given is left empty.
	Roles map[string]string
}

const (
	// jobWait is how long a job is waited for before Made.Pending.
	jobWait = 60 * time.Second
	// jobPoll is the first pause between looks at a job; it doubles up to jobPollMax.
	jobPoll    = 500 * time.Millisecond
	jobPollMax = 5 * time.Second
)

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// TaskTemplates lists the task templates of a project (online). Names are made safe for
// a terminal.
func (s *Syncer) TaskTemplates(ctx context.Context, projectGID string) ([]model.TaskTemplate, error) {
	if s.API == nil {
		return nil, ErrOffline
	}
	list, err := s.API.TaskTemplates(ctx, projectGID)
	if err := s.noteTemplatePlan(ctx, plan.TaskTemplates, s.projectWorkspace(ctx, projectGID), err); err != nil {
		return nil, err
	}
	for i := range list {
		list[i].Name = termsafe.String(list[i].Name)
	}
	return list, nil
}

// ProjectTemplates lists the project templates of the default workspace (online), or of
// team teamGID. Names are made safe for a terminal.
func (s *Syncer) ProjectTemplates(ctx context.Context, teamGID string) ([]model.ProjectTemplate, error) {
	if s.API == nil {
		return nil, ErrOffline
	}
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	list, err := s.API.ProjectTemplates(ctx, ws, teamGID)
	if teamGID == "" && needsTeam(err) {
		// An organization lists templates by team only (400
		// requires_workspace_received_organization): those of each team I am in.
		list, err = s.teamProjectTemplates(ctx, ws)
	}
	if err := s.noteTemplatePlan(ctx, plan.ProjectTemplates, ws, err); err != nil {
		return nil, err
	}
	for i := range list {
		t := &list[i]
		t.Name, t.Description = termsafe.String(t.Name), termsafe.String(t.Description)
		if t.Team != nil {
			t.Team.Name = termsafe.String(t.Team.Name)
		}
		for j := range t.RequestedDates {
			d := &t.RequestedDates[j]
			d.Name, d.Description = termsafe.String(d.Name), termsafe.String(d.Description)
		}
		for j := range t.RequestedRoles {
			t.RequestedRoles[j].Name = termsafe.String(t.RequestedRoles[j].Name)
		}
	}
	return list, nil
}

// teamProjectTemplates lists the project templates of every team I am in, each once.
func (s *Syncer) teamProjectTemplates(ctx context.Context, ws string) ([]model.ProjectTemplate, error) {
	teams, err := s.API.MyTeams(ctx, ws)
	if err != nil {
		return nil, err
	}
	var out []model.ProjectTemplate
	seen := map[string]bool{}
	for _, t := range teams {
		list, err := s.API.ProjectTemplates(ctx, ws, t.GID)
		if err != nil {
			return nil, err
		}
		for _, pt := range list {
			if !seen[pt.GID] {
				seen[pt.GID] = true
				if pt.Team == nil {
					pt.Team = &model.Ref{GID: t.GID, Name: t.Name}
				}
				out = append(out, pt)
			}
		}
	}
	return out, nil
}

// needsTeam reports whether Asana refused a workspace because it is an organization.
func needsTeam(err error) bool {
	var apiErr *model.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		return false
	}
	for _, e := range apiErr.Errors {
		if e.Error == "requires_workspace_received_organization" {
			return true
		}
	}
	return false
}

// projectWorkspace is the workspace of a project, whose plan a 402 for its task templates
// is about (the default workspace when the project is not cached).
func (s *Syncer) projectWorkspace(ctx context.Context, projectGID string) string {
	if ws, err := s.Store.ProjectWorkspace(ctx, projectGID); err == nil && ws != "" {
		return ws
	}
	ws, _ := s.Store.DefaultWorkspace(ctx)
	return ws
}

// noteTemplatePlan records what err says about the plan of workspace workspaceGID (the
// three helpers of plan.go): a 402 records that feature is refused there and is returned
// as a TemplatePlanError, which says so; an answer forgets an earlier 402. Other errors are
// returned as they are.
func (s *Syncer) noteTemplatePlan(ctx context.Context, feature, workspaceGID string, err error) error {
	if err == nil {
		_ = s.clearPlanRefusal(ctx, feature, workspaceGID)
		return nil
	}
	if refused, _ := s.recordPlanRefusal(ctx, feature, workspaceGID, err); refused {
		return &TemplatePlanError{Feature: feature, Err: err}
	}
	return err
}

// TemplatePlanError is a 402 for templates: the workspace's plan lacks them.
type TemplatePlanError struct {
	Feature string
	Err     error
}

func (e *TemplatePlanError) Error() string {
	what := map[string]string{
		plan.TaskTemplates:    "タスクテンプレート",
		plan.ProjectTemplates: "プロジェクトテンプレート",
	}[e.Feature]
	return "このワークスペースのプランでは、" + what + "を API で使えません (Asana が 402 を返しました)"
}
func (e *TemplatePlanError) Unwrap() error { return e.Err }

// PickTemplate finds a template by key in a list: its gid, its exact name, or the one
// name that contains key (ignoring case). name returns a template's name.
func PickTemplate[T any](list []T, key string, gid, name func(T) string) (T, error) {
	var zero T
	key = strings.TrimSpace(key)
	if key == "" {
		return zero, errors.New("テンプレートが指定されていません")
	}
	for _, t := range list {
		if gid(t) == key {
			return t, nil
		}
	}
	for _, t := range list {
		if name(t) == key {
			return t, nil
		}
	}
	var found []T
	lower := strings.ToLower(key)
	for _, t := range list {
		if strings.Contains(strings.ToLower(name(t)), lower) {
			found = append(found, t)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return zero, fmt.Errorf("テンプレート「%s」が見つかりません", termsafe.String(key))
	}
	names := make([]string, len(found))
	for i, t := range found {
		names[i] = name(t)
	}
	return zero, fmt.Errorf("テンプレート「%s」に当てはまるものが複数あります: %s", termsafe.String(key), strings.Join(names, " / "))
}

// InstantiateTask makes a task from template tmpl (from TaskTemplates) in its project,
// named name ("" for the template's name), and stores it with its subtasks.
func (s *Syncer) InstantiateTask(ctx context.Context, tmpl model.TaskTemplate, name string) (*Made, error) {
	if s.API == nil {
		return nil, ErrOffline
	}
	if strings.TrimSpace(name) == "" {
		name = tmpl.Name // always named, so that a dropped connection can be checked
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("タスク名が空です")
	}
	started := s.clock()
	job, err := s.API.InstantiateTask(ctx, tmpl.GID, name)
	if err != nil {
		ws := ""
		if tmpl.Project != nil {
			ws = s.projectWorkspace(ctx, tmpl.Project.GID)
		}
		err = s.noteTemplatePlan(ctx, plan.TaskTemplates, ws, err)
		var pe *TemplatePlanError
		if errors.As(err, &pe) || tmpl.Project == nil {
			return nil, err
		}
		if notMade(ctx, err) {
			return nil, err
		}
		return s.foundTask(ctx, tmpl.Project.GID, name, started, err,
			"タスクが作られたか、まだ分かりません。次の同期の後にプロジェクトに無ければ、もう一度作ってください")
	}
	job, done, err := s.waitJob(ctx, job, func(j *model.Job) bool { return j.NewTask != nil })
	if err != nil {
		// Asana took the job but asagao does not know what it made yet: look for it.
		err = fmt.Errorf("%s: %w", jobAccepted, err)
		if tmpl.Project == nil {
			return nil, err
		}
		return s.foundTask(ctx, tmpl.Project.GID, name, started, err, "")
	}
	if job.NewTask == nil {
		return nil, jobError(job, "タスク")
	}
	made := &Made{GID: job.NewTask.GID, Name: name, Pending: !done}
	if err := s.FetchDetails(ctx, made.GID); err != nil && !isAttachmentsError(err) {
		return made, fmt.Errorf("タスクは作られましたが、取り込めませんでした (次の同期で取り込みます): %w", err)
	}
	if job.Status == model.JobFailed {
		return made, jobError(job, "タスク")
	}
	return made, nil
}

func isAttachmentsError(err error) bool {
	var ae *AttachmentsError
	return errors.As(err, &ae)
}

// jobAccepted says that Asana took a job whose result is not known yet.
const jobAccepted = "Asana は作る処理を受け付けましたが、できたものをまだ確かめられません。作り直さないでください (できたものは次の同期で取り込みます)"

// foundTask looks for the task that a failed or unfinished instantiateTask may have made
// (findMadeTask) and stores it, as Pending: the job's state is unknown, so the subtasks
// may still be coming.
func (s *Syncer) foundTask(ctx context.Context, projectGID, name string, started time.Time, cause error, notFound string) (*Made, error) {
	t, err := s.findMadeTask(ctx, projectGID, name, started, cause, notFound)
	if err != nil {
		return nil, err
	}
	made := &Made{GID: t.GID, Name: name, Pending: true}
	if err := s.FetchDetails(ctx, t.GID); err != nil && !isAttachmentsError(err) {
		return made, fmt.Errorf("タスクは作られましたが、取り込めませんでした (次の同期で取り込みます): %w", err)
	}
	return made, nil
}

// findMadeTask looks for the task of that name created in the project since started
// (twice, projectSettle apart). Not found, it is not made again (that could make a
// duplicate): the error is notFound wrapping cause (cause alone if notFound is "").
func (s *Syncer) findMadeTask(ctx context.Context, projectGID, name string, started time.Time, cause error, notFound string) (*model.Task, error) {
	for try := 0; try < 2; try++ {
		if try > 0 {
			if err := s.wait(ctx, projectSettle); err != nil {
				return nil, cause
			}
		}
		since := started.Add(s.API.ClockOffset()).Add(-clockSkew)
		tasks, err := s.API.Tasks(ctx, model.TaskQuery{Project: projectGID, ModifiedSince: since})
		if err != nil {
			return nil, fmt.Errorf("タスクが作られたか確かめられませんでした (%v): %w", err, cause)
		}
		t, m := pick(tasks, func(t model.Task) bool { return t.Name == name && !t.CreatedAt.Before(since) })
		switch m {
		case matched:
			return &t, nil
		case ambiguous:
			return nil, fmt.Errorf("同じ名前のタスクが複数作られています。Asana で確認してください: %w", cause)
		}
	}
	if notFound == "" {
		return nil, cause
	}
	return nil, fmt.Errorf("%s: %w", notFound, cause)
}

// InstantiateProject makes a project from template tmpl (from ProjectTemplates) and
// stores it with its sections and tasks.
func (s *Syncer) InstantiateProject(ctx context.Context, tmpl model.ProjectTemplate, in ProjectInput) (*Made, error) {
	if s.API == nil {
		return nil, ErrOffline
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, errors.New("プロジェクト名が空です")
	}
	dates, err := s.requestedDates(tmpl, in.Dates)
	if err != nil {
		return nil, err
	}
	roles, err := requestedRoles(tmpl, in.Roles)
	if err != nil {
		return nil, err
	}
	return s.makeProject(ctx, in.Name, plan.ProjectTemplates, func() (*model.Job, error) {
		return s.API.InstantiateProject(ctx, tmpl.GID, model.ProjectFromTemplate{
			Name: in.Name, TeamGID: in.TeamGID, Public: in.Public, Dates: dates, Roles: roles})
	})
}

// DuplicateProject duplicates project projectGID (with its tasks, subtasks, members,
// dates, …: asana.ProjectDuplicateIncludes) as name into team teamGID ("" for the
// original's team), and stores the copy with its sections and tasks.
func (s *Syncer) DuplicateProject(ctx context.Context, projectGID, name, teamGID string) (*Made, error) {
	if s.API == nil {
		return nil, ErrOffline
	}
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("プロジェクト名が空です")
	}
	return s.makeProject(ctx, name, "", func() (*model.Job, error) {
		return s.API.DuplicateProject(ctx, projectGID, name, teamGID)
	})
}

// makeProject starts a job that makes a project named name, waits for it and stores the
// project. feature is the plan feature a 402 marks ("" for none).
func (s *Syncer) makeProject(ctx context.Context, name, feature string, start func() (*model.Job, error)) (*Made, error) {
	ws, err := s.Store.DefaultWorkspace(ctx)
	if err != nil {
		return nil, err
	}
	started := s.clock()
	job, err := start()
	if err != nil {
		if feature != "" {
			err = s.noteTemplatePlan(ctx, feature, ws, err)
		}
		var pe *TemplatePlanError
		if errors.As(err, &pe) {
			return nil, err
		}
		p, rerr := s.recoverCreateProject(ctx, ws, name, started, err)
		if rerr != nil {
			return nil, rerr
		}
		// Found: the job's state is unknown, so its tasks may still be coming.
		return s.storeMadeProject(ctx, p.GID, name, true)
	}
	job, done, err := s.waitJob(ctx, job, func(j *model.Job) bool { return j.NewProject != nil })
	if err != nil {
		// Asana took the job but asagao does not know what it made yet: look for it.
		err = fmt.Errorf("%s: %w", jobAccepted, err)
		p, err := s.findCreatedProject(ctx, ws, name, started, err, "")
		if err != nil {
			return nil, err
		}
		return s.storeMadeProject(ctx, p.GID, name, true)
	}
	if job.NewProject == nil {
		return nil, jobError(job, "プロジェクト")
	}
	made, err := s.storeMadeProject(ctx, job.NewProject.GID, name, !done)
	if err == nil && job.Status == model.JobFailed {
		err = jobError(job, "プロジェクト")
	}
	return made, err
}

// storeMadeProject fetches a project that a job made, with its sections and tasks, and
// stores it.
func (s *Syncer) storeMadeProject(ctx context.Context, gid, name string, pending bool) (*Made, error) {
	made := &Made{GID: gid, Name: name, Pending: pending}
	p, err := s.API.Project(ctx, gid)
	if err == nil {
		made.Name = termsafe.String(p.Name)
		if err = s.saveProject(ctx, p); err == nil {
			var secs []model.Section
			if secs, err = s.API.Sections(ctx, gid); err == nil {
				err = s.Store.ReplaceSections(ctx, gid, secs)
			}
		}
	}
	if err == nil {
		var tasks []model.Task
		if tasks, err = s.API.Tasks(ctx, model.TaskQuery{Project: gid}); err == nil {
			err = s.Store.UpsertTasks(ctx, tasks)
		}
	}
	if err != nil {
		return made, fmt.Errorf("プロジェクトは作られましたが、取り込めませんでした (次の同期で取り込みます): %w", err)
	}
	return made, nil
}

// waitJob looks at job until it is done, up to jobWait, and returns it as last seen.
// has reports whether the job already names what it makes; a job that does is not waited
// for past jobWait, one that does not yet is an error then. done is whether it ended.
func (s *Syncer) waitJob(ctx context.Context, job *model.Job, has func(*model.Job) bool) (_ *model.Job, done bool, _ error) {
	pause, waited := jobPoll, time.Duration(0)
	for !job.Done() {
		if job.GID == "" || waited >= jobWait {
			if has(job) {
				return job, false, nil
			}
			return nil, false, fmt.Errorf("%v たっても終わりませんでした", jobWait)
		}
		if err := s.wait(ctx, pause); err != nil {
			if has(job) {
				return job, false, nil // stopped waiting; what is made is stored
			}
			return nil, false, err
		}
		waited += pause
		pause = min(pause*2, jobPollMax)
		next, err := s.API.Job(ctx, job.GID)
		if err != nil {
			if has(job) {
				return job, false, nil // the rest is fetched by the next sync
			}
			return nil, false, fmt.Errorf("Asana の処理の状態を確かめられませんでした: %w", err)
		}
		if next.NewTask == nil {
			next.NewTask = job.NewTask
		}
		if next.NewProject == nil {
			next.NewProject = job.NewProject
		}
		job = next
	}
	return job, true, nil
}

func jobError(job *model.Job, what string) error {
	if job.Status == model.JobFailed {
		return fmt.Errorf("Asana で%sを作る処理が失敗しました", what)
	}
	return fmt.Errorf("Asana が作った%sを返しませんでした", what)
}

// requestedDates fills the template's requested dates: given ones (by gid or name) must
// be dates YYYY-MM-DD, the others are today.
func (s *Syncer) requestedDates(tmpl model.ProjectTemplate, given map[string]string) (map[string]string, error) {
	out, err := byGIDOrName(tmpl.RequestedDates, given, func(d model.TemplateDate) (string, string) { return d.GID, d.Name }, "日付")
	if err != nil {
		return nil, err
	}
	for _, d := range tmpl.RequestedDates {
		v, ok := out[d.GID]
		if !ok {
			out[d.GID] = s.clock().Format(time.DateOnly)
			continue
		}
		if _, err := time.Parse(time.DateOnly, v); err != nil || !dateRE.MatchString(v) {
			return nil, fmt.Errorf("日付「%s」は YYYY-MM-DD の日付で書いてください: %s", d.Name, termsafe.String(v))
		}
	}
	return out, nil
}

// requestedRoles maps the given roles (by gid or name) to the template's role gids.
func requestedRoles(tmpl model.ProjectTemplate, given map[string]string) (map[string]string, error) {
	return byGIDOrName(tmpl.RequestedRoles, given, func(r model.Ref) (string, string) { return r.GID, r.Name }, "役割")
}

// byGIDOrName maps values given by an item's gid or name to the item's gid. The same item
// given both ways must have the same value; a key that is no item is an error.
func byGIDOrName[T any](items []T, given map[string]string, keys func(T) (gid, name string), what string) (map[string]string, error) {
	out := map[string]string{}
	used := map[string]bool{}
	for _, it := range items {
		gid, name := keys(it)
		for _, k := range []string{gid, name} {
			v, ok := given[k]
			if !ok || k == "" {
				continue
			}
			if prev, dup := out[gid]; dup && prev != v {
				return nil, fmt.Errorf("%s「%s」に違う値が 2 つあります: %s / %s", what, name, termsafe.String(prev), termsafe.String(v))
			}
			out[gid], used[k] = v, true
		}
	}
	for k := range given {
		if !used[k] {
			return nil, fmt.Errorf("テンプレートに%s「%s」はありません", what, termsafe.String(k))
		}
	}
	return out, nil
}
