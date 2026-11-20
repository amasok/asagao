package sync

import (
	"context"
	"sync/atomic"
	"time"
)

// Pusher sends the outbox to Asana (Push). Make one with Syncer.Pusher, or set its fields.
type Pusher struct {
	env
	API   PushAPI
	Store PushStore
	Cfg   Config
	// RichText: see Syncer.RichText.
	RichText RichText
	// PullOnly: see Syncer.PullOnly.
	PullOnly bool
	// accountOK is shared with the Syncer the Pusher was made from (nil: asked each time).
	accountOK *atomic.Bool
}

// Puller brings the cache up to date (Run, OpenProject, FetchCompleted). Pusher, which Run
// sends the outbox with and which holds the push lock, must be set.
type Puller struct {
	env
	API    PullAPI
	Store  PullStore
	Cfg    Config
	Pusher *Pusher
	// Progress receives human-readable progress messages (optional).
	Progress func(format string, args ...any)
	// PullOnly and DeferCompleted: see Syncer.
	PullOnly       bool
	DeferCompleted bool
}

// progress sends a progress message, if anyone listens.
func (s *Puller) progress(format string, args ...any) {
	if s.Progress != nil {
		s.Progress(format, args...)
	}
}

// Details fetches a task's details (comments, attachments, time entries, dependencies) and
// a project's overview and status updates, when they are shown.
type Details struct {
	env
	API   DetailsAPI
	Store DetailsStore
	// CacheDir and Download: see Syncer.
	CacheDir string
	Download func(ctx context.Context, url string) ([]byte, error)
}

// Goals fetches goals and portfolios.
type Goals struct {
	env
	API   GoalsAPI
	Store GoalsStore
}

// Templates lists task and project templates, and makes tasks and projects from them.
// Details (to fetch what a task was made with) and Projects (to find a project made) must
// be set.
type Templates struct {
	env
	API      TemplatesAPI
	Store    TemplatesStore
	Details  *Details
	Projects *Projects
}

// Search finds in Asana what the cache does not have (tasks, users, tags).
type Search struct {
	env
	API   SearchAPI
	Store SearchStore
}

// Projects makes and changes projects online.
type Projects struct {
	env
	API   ProjectsAPI
	Store ProjectsStore
}

// accountConfirmed reports whether Asana answered as the account the local data belongs
// to, for this Pusher (or the Syncer it was made from) already.
func (s *Pusher) accountConfirmed() bool { return s.accountOK != nil && s.accountOK.Load() }

// confirmAccount records that Asana answered as the account the local data belongs to.
func (s *Pusher) confirmAccount() {
	if s.accountOK != nil {
		s.accountOK.Store(true)
	}
}

// serverNow is the current time by Asana's clock (see env.serverNowBy).
func (s *Pusher) serverNow() time.Time { return s.serverNowBy(s.API) }

// serverNow is the current time by Asana's clock (see env.serverNowBy).
func (s *Puller) serverNow() time.Time { return s.serverNowBy(s.API) }

// serverNow is the current time by Asana's clock (see env.serverNowBy).
func (s *Details) serverNow() time.Time { return s.serverNowBy(s.API) }
