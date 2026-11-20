package app

import (
	"context"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// Ports are the stores the use cases write and read through, one for each use case.
// *store.Store fills them all; internal/wire sets them, and the entry points see only the
// use cases (UseCases), never the store as a whole.
type Ports struct {
	Tasks         TaskStore
	Comments      CommentStore
	Status        StatusStore
	Briefs        BriefStore
	Projects      ProjectStore
	Relations     RelationStore
	Sections      SectionStore
	TaskOps       TaskOpsStore
	Tags          TagStore
	Copies        CopyStore
	FieldOptions  FieldOptionStore
	ProjectFields ProjectFieldStore
	Notifications NotificationStore
	Outbox        OutboxStore
	Goals         GoalStore
	TimeTracking  TimeTrackingStore
	Push          PushStore
	GitLink       GitLinkStore
	CustomIDs     CustomIDStore
	QuickAdd      QuickAddStore
	Query         QueryStore
	ProjectOpen   ProjectOpenStore
	ViewState     ViewStateStore
	Reset         ResetStore
}

// ProjectOpenStore records that a project was opened: from then on, a sync fetches its
// tasks (#54).
type ProjectOpenStore interface {
	// WantProject records it and returns the state of the project's tasks in the cache.
	WantProject(ctx context.Context, projectGID string) (string, error)
}

// ViewStateStore keeps how the TUI was left: the groups folded in each list and the
// layout of its panes.
type ViewStateStore interface {
	CollapsedGroups(ctx context.Context, scope string) (string, error)
	SetCollapsedGroups(ctx context.Context, scope, raw string) error
	LayoutState(ctx context.Context) (string, error)
	SetLayoutState(ctx context.Context, raw string) error
}

// ResetStore deletes the local data, after writing the unsent changes up to entry upTo
// to a backup in backupDir.
type ResetStore interface {
	ResetWithBackup(ctx context.Context, backupDir string, upTo int64, now time.Time) (readmodel.ResetResult, error)
}

// Text is the rich text conversion the use cases write with (richtext.Converter).
type Text interface {
	RichText
	BriefText
}

// UseCases make the use cases over one store and one rich text conversion. internal/wire
// puts them together and hands them to the entry points, which neither ask for the store
// as a whole nor convert rich text themselves. Each use case gets the clock it is to read:
// the TUI stops it at the time a key was pressed, the CLI and MCP read time.Now.
type UseCases struct {
	ports Ports
	text  Text
}

// NewUseCases makes the use cases over ports, writing rich text with text.
func NewUseCases(ports Ports, text Text) *UseCases {
	return &UseCases{ports: ports, text: text}
}

// WantProject records that a project was opened (ProjectOpenStore).
func (u *UseCases) WantProject(ctx context.Context, projectGID string) (string, error) {
	return u.ports.ProjectOpen.WantProject(ctx, projectGID)
}

// ViewState is where the TUI keeps how it was left.
func (u *UseCases) ViewState() ViewStateStore { return u.ports.ViewState }

// Reset deletes the local data (ResetStore).
func (u *UseCases) Reset(ctx context.Context, backupDir string, upTo int64, now time.Time) (readmodel.ResetResult, error) {
	return u.ports.Reset.ResetWithBackup(ctx, backupDir, upTo, now)
}

// Query reads the local cache.
func (u *UseCases) Query() Query { return Query{QueryStore: u.ports.Query} }

// Tasks is the task use cases (names: see Tasks.Names; nil looks nothing up in Asana).
func (u *UseCases) Tasks(clock Clock, names NameFinder) *Tasks {
	return &Tasks{Store: u.ports.Tasks, Text: u.text, Clock: clock, Names: names}
}

// Comments is the comment use cases.
func (u *UseCases) Comments(clock Clock) *Comments {
	return &Comments{Store: u.ports.Comments, Text: u.text, Clock: clock}
}

// Status is the status update use cases.
func (u *UseCases) Status(clock Clock) *Status {
	return &Status{Store: u.ports.Status, Text: u.text, Clock: clock}
}

// Briefs is the project brief use cases.
func (u *UseCases) Briefs(clock Clock) *Briefs {
	return &Briefs{Store: u.ports.Briefs, Text: u.text, Clock: clock}
}

// Projects is the project use cases.
func (u *UseCases) Projects(clock Clock) *Projects {
	return &Projects{Store: u.ports.Projects, Text: u.text, Clock: clock}
}

// Relations is the use cases on a task's relations.
func (u *UseCases) Relations(clock Clock) *Relations {
	return &Relations{Store: u.ports.Relations, Clock: clock}
}

// Sections is the use cases on sections.
func (u *UseCases) Sections(clock Clock) *Sections {
	return &Sections{Store: u.ports.Sections, Clock: clock}
}

// TaskOps is the use cases that move, complete or delete tasks.
func (u *UseCases) TaskOps(clock Clock) *TaskOps {
	return &TaskOps{Store: u.ports.TaskOps, Clock: clock}
}

// Tags is the tag use cases.
func (u *UseCases) Tags(clock Clock) *Tags { return &Tags{Store: u.ports.Tags, Clock: clock} }

// Copies is the duplicate and attach use cases; files to upload are copied to cacheDir.
func (u *UseCases) Copies(cacheDir string, clock Clock) *Copies {
	return &Copies{Store: u.ports.Copies, CacheDir: cacheDir, Clock: clock}
}

// FieldOptions is the use cases on a custom field's options.
func (u *UseCases) FieldOptions(clock Clock) *FieldOptions {
	return &FieldOptions{Store: u.ports.FieldOptions, Clock: clock}
}

// ProjectFields is the use cases on a project's custom fields.
func (u *UseCases) ProjectFields(clock Clock) *ProjectFields {
	return &ProjectFields{Store: u.ports.ProjectFields, Clock: clock}
}

// Notifications is the inbox use cases.
func (u *UseCases) Notifications(clock Clock) *Notifications {
	return &Notifications{Store: u.ports.Notifications, Clock: clock}
}

// Outbox is the use cases on queued changes.
func (u *UseCases) Outbox() *Outbox { return &Outbox{Store: u.ports.Outbox} }

// Goals is the goal and portfolio use cases.
func (u *UseCases) Goals(clock Clock) *Goals { return &Goals{Store: u.ports.Goals, Clock: clock} }

// TimeTracking is the time entry use cases.
func (u *UseCases) TimeTracking(clock Clock) *TimeTracking {
	return &TimeTracking{Store: u.ports.TimeTracking, Clock: clock}
}

// PushNow sends the outbox at once (loginHelp: see PushNow.LoginHelp).
func (u *UseCases) PushNow(loginHelp func(err error) string) PushNow {
	return PushNow{Store: u.ports.Push, LoginHelp: loginHelp}
}

// GitLink links commits to the tasks they reference (customIDs: see GitLink.CustomIDs).
func (u *UseCases) GitLink(clock Clock, customIDs map[string]string) GitLink {
	return GitLink{Store: u.ports.GitLink, Text: u.text, Clock: clock, CustomIDs: customIDs}
}

// ResolveCustomIDs: see the function ResolveCustomIDs.
func (u *UseCases) ResolveCustomIDs(find CustomIDFinder) func(ctx context.Context, ids []string) (map[string]string, error) {
	return func(ctx context.Context, ids []string) (map[string]string, error) {
		return ResolveCustomIDs(ctx, u.ports.CustomIDs, find, ids)
	}
}

// QuickAdd reads quick-add lines against the cache.
func (u *UseCases) QuickAdd() QuickAdd { return QuickAdd{Store: u.ports.QuickAdd} }

// QuickAdd reads quick-add lines (ReadQuickAdd and the functions next to it) against Store.
type QuickAdd struct {
	Store QuickAddStore
}

// Candidates: see LoadQuickAddCandidates.
func (q QuickAdd) Candidates(ctx context.Context) (QuickAddCandidates, error) {
	return LoadQuickAddCandidates(ctx, q.Store)
}

// Read: see ReadQuickAdd.
func (q QuickAdd) Read(ctx context.Context, line string, now time.Time) (QuickAddResult, error) {
	return ReadQuickAdd(ctx, q.Store, line, now)
}

// ReadFinding: see ReadQuickAddFinding.
func (q QuickAdd) ReadFinding(ctx context.Context, find NameFinder, line string, now time.Time) (QuickAddResult, error) {
	return ReadQuickAddFinding(ctx, q.Store, find, line, now)
}
