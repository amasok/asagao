package sync

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
)

// A sync is made of parts, each asking only for the ports it uses (ports.go):
//
//   - Pusher sends the outbox (Push).
//   - Puller brings the cache up to date (Run, OpenProject, FetchCompleted); Run sends the
//     outbox first, through its Pusher.
//   - Details, Goals, Templates, Search and Projects fetch or change what the user asks
//     for, when asked (a task's details, goals and portfolios, templates, a search in
//     Asana, a project made or changed online).
//
// Syncer puts them together for one API client and one store, which is how the entry
// points use them (internal/wire builds it).

// env is what the parts share: the clock and waiting (both replaced in tests).
type env struct {
	now   func() time.Time
	sleep func(context.Context, time.Duration) error
}

// clock is the current time on this machine (replaced in tests).
func (e env) clock() time.Time {
	if e.now != nil {
		return e.now()
	}
	return time.Now()
}

// wait pauses for d (or until ctx is done).
func (e env) wait(ctx context.Context, d time.Duration) error {
	if e.sleep != nil {
		return e.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// serverNowBy is the current time by Asana's clock (as far as the responses of api so far
// tell): what is compared with Asana's timestamps or sent as modified_since must not
// depend on this machine's clock being right. A nil api (offline) leaves the clock as it is.
func (e env) serverNowBy(api ClockAPI) time.Time {
	if api == nil {
		return e.clock()
	}
	return e.clock().Add(api.ClockOffset())
}

// Syncer is a sync for one API client and one store: it makes the parts above from its
// fields when one is used, so a field changed after it was made applies to the next call.
type Syncer struct {
	API   AsanaAPI
	Store LocalStore
	Cfg   Config
	// RichText rewrites comments and status updates queued before #63 before Push sends
	// them. The caller must set it (richtext.Converter{}): without it, Push fails with
	// store.ErrNoFit when there is such an entry to check, rather than send it unchecked.
	RichText RichText
	// Progress receives human-readable progress messages (optional).
	Progress func(format string, args ...any)
	// CacheDir is where attachments are downloaded (default: xdg.CacheDir()/attachments).
	CacheDir string
	// Download fetches a pre-signed attachment URL. It must not add the Asana
	// Authorization header (the storage service rejects it). Default: plain HTTP GET.
	Download func(ctx context.Context, url string) ([]byte, error)
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error // replaced in tests
	// PullOnly makes Run only fetch from Asana: queued local changes are left in the
	// outbox (they stay protected from being overwritten) and Push refuses to send.
	// Used by the read-only MCP server, so an agent cannot send what the user queued.
	PullOnly bool
	// DeferCompleted makes Run leave the completed tasks of what it fetches for the first
	// time (My Tasks, a project opened) to FetchCompleted (Result.CompletedPending), so that
	// the incomplete ones can be shown first. Without it, Run fetches them last.
	DeferCompleted bool
	// accountOK is set once Asana answered as the account the local data belongs to, so
	// Push asks /users/me only once per Syncer (see Pusher.checkPushAccount).
	accountOK atomic.Bool
}

func (s *Syncer) env() env {
	return env{now: s.now, sleep: s.sleep}
}

// clock is the current time on this machine (replaced in tests).
func (s *Syncer) clock() time.Time { return s.env().clock() }

// Pusher is the part of s that sends the outbox.
func (s *Syncer) Pusher() *Pusher {
	return &Pusher{env: s.env(), API: s.API, Store: s.Store, Cfg: s.Cfg, RichText: s.RichText,
		PullOnly: s.PullOnly, accountOK: &s.accountOK}
}

// Puller is the part of s that brings the cache up to date.
func (s *Syncer) Puller() *Puller {
	return &Puller{env: s.env(), API: s.API, Store: s.Store, Cfg: s.Cfg, Pusher: s.Pusher(),
		Progress: s.Progress, PullOnly: s.PullOnly, DeferCompleted: s.DeferCompleted}
}

// Details is the part of s that fetches a task's or a project's details when they are shown.
func (s *Syncer) Details() *Details {
	return &Details{env: s.env(), API: s.API, Store: s.Store, CacheDir: s.CacheDir, Download: s.Download}
}

// Goals is the part of s that fetches goals and portfolios.
func (s *Syncer) Goals() *Goals { return &Goals{env: s.env(), API: s.API, Store: s.Store} }

// Templates is the part of s that lists and uses task and project templates.
func (s *Syncer) Templates() *Templates {
	return &Templates{env: s.env(), API: s.API, Store: s.Store, Details: s.Details(), Projects: s.Projects()}
}

// Search is the part of s that finds in Asana what the cache does not have.
func (s *Syncer) Search() *Search { return &Search{env: s.env(), API: s.API, Store: s.Store} }

// Projects is the part of s that makes and changes projects online.
func (s *Syncer) Projects() *Projects { return &Projects{env: s.env(), API: s.API, Store: s.Store} }

// Run: see Puller.Run.
func (s *Syncer) Run(ctx context.Context, forceFull bool) (Result, error) {
	return s.Puller().Run(ctx, forceFull)
}

// OpenProject: see Puller.OpenProject.
func (s *Syncer) OpenProject(ctx context.Context, projectGID string, withCompleted bool) (OpenResult, error) {
	return s.Puller().OpenProject(ctx, projectGID, withCompleted)
}

// FetchCompleted: see Puller.FetchCompleted.
func (s *Syncer) FetchCompleted(ctx context.Context) (Result, error) {
	return s.Puller().FetchCompleted(ctx)
}

// Push: see Pusher.Push.
func (s *Syncer) Push(ctx context.Context) (PushResult, error) { return s.Pusher().Push(ctx) }

// FetchDetails: see Details.FetchDetails.
func (s *Syncer) FetchDetails(ctx context.Context, taskGID string) error {
	return s.Details().FetchDetails(ctx, taskGID)
}

// FetchTimeEntries: see Details.FetchTimeEntries.
func (s *Syncer) FetchTimeEntries(ctx context.Context, taskGID string) error {
	return s.Details().FetchTimeEntries(ctx, taskGID)
}

// DownloadAttachment: see Details.DownloadAttachment.
func (s *Syncer) DownloadAttachment(ctx context.Context, taskGID, gid string) (string, error) {
	return s.Details().DownloadAttachment(ctx, taskGID, gid)
}

// FetchStatusUpdates: see Details.FetchStatusUpdates.
func (s *Syncer) FetchStatusUpdates(ctx context.Context, projectGID string) error {
	return s.Details().FetchStatusUpdates(ctx, projectGID)
}

// FetchProjectOverview: see Details.FetchProjectOverview.
func (s *Syncer) FetchProjectOverview(ctx context.Context, projectGID string) error {
	return s.Details().FetchProjectOverview(ctx, projectGID)
}

// FetchGoals: see Goals.FetchGoals.
func (s *Syncer) FetchGoals(ctx context.Context) error { return s.Goals().FetchGoals(ctx) }

// FetchGoal: see Goals.FetchGoal.
func (s *Syncer) FetchGoal(ctx context.Context, gid string) error {
	return s.Goals().FetchGoal(ctx, gid)
}

// FetchPortfolios: see Goals.FetchPortfolios.
func (s *Syncer) FetchPortfolios(ctx context.Context) error { return s.Goals().FetchPortfolios(ctx) }

// FetchPortfolio: see Goals.FetchPortfolio.
func (s *Syncer) FetchPortfolio(ctx context.Context, gid string) error {
	return s.Goals().FetchPortfolio(ctx, gid)
}

// TaskTemplates: see Templates.TaskTemplates.
func (s *Syncer) TaskTemplates(ctx context.Context, projectGID string) ([]model.TaskTemplate, error) {
	return s.Templates().TaskTemplates(ctx, projectGID)
}

// ProjectTemplates: see Templates.ProjectTemplates.
func (s *Syncer) ProjectTemplates(ctx context.Context, teamGID string) ([]model.ProjectTemplate, error) {
	return s.Templates().ProjectTemplates(ctx, teamGID)
}

// InstantiateTask: see Templates.InstantiateTask.
func (s *Syncer) InstantiateTask(ctx context.Context, tmpl model.TaskTemplate, name string) (*Made, error) {
	return s.Templates().InstantiateTask(ctx, tmpl, name)
}

// InstantiateProject: see Templates.InstantiateProject.
func (s *Syncer) InstantiateProject(ctx context.Context, tmpl model.ProjectTemplate, in ProjectInput) (*Made, error) {
	return s.Templates().InstantiateProject(ctx, tmpl, in)
}

// DuplicateProject: see Templates.DuplicateProject.
func (s *Syncer) DuplicateProject(ctx context.Context, projectGID, name, teamGID string) (*Made, error) {
	return s.Templates().DuplicateProject(ctx, projectGID, name, teamGID)
}

// SearchTasks: see Search.SearchTasks.
func (s *Syncer) SearchTasks(ctx context.Context, text string, completed *bool, limit int) (SearchResult, error) {
	return s.Search().SearchTasks(ctx, text, completed, limit)
}

// FindUsers: see Search.FindUsers.
func (s *Syncer) FindUsers(ctx context.Context, query string) (int, error) {
	return s.Search().FindUsers(ctx, query)
}

// FindTags: see Search.FindTags.
func (s *Syncer) FindTags(ctx context.Context, query string) (int, error) {
	return s.Search().FindTags(ctx, query)
}

// TaskByCustomID: see Search.TaskByCustomID.
func (s *Syncer) TaskByCustomID(ctx context.Context, customID string) (string, error) {
	return s.Search().TaskByCustomID(ctx, customID)
}

// CreateProject: see Projects.CreateProject.
func (s *Syncer) CreateProject(ctx context.Context, name, teamGID string) (*model.Project, error) {
	return s.Projects().CreateProject(ctx, name, teamGID)
}

// SetProjectArchived: see Projects.SetProjectArchived.
func (s *Syncer) SetProjectArchived(ctx context.Context, gid string, archived bool) error {
	return s.Projects().SetProjectArchived(ctx, gid, archived)
}

// SetProjectMembers: see Projects.SetProjectMembers.
func (s *Syncer) SetProjectMembers(ctx context.Context, gid string, add, remove []string) error {
	return s.Projects().SetProjectMembers(ctx, gid, add, remove)
}
