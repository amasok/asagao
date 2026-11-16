package app

import "context"

// Status is the project status update use cases.
type Status struct {
	Store StatusStore
	Text  RichText
	Clock Clock
}

// StatusInput is a status update to post. Status is its type (on_track, at_risk, …) and
// Text its body in Markdown. Project is a name or a gid for Create, a gid for CreateHere.
type StatusInput struct {
	Project, Status, Title, Text string
}

// Create resolves the project by name or gid and posts the status update to it as me,
// locally and queued in the outbox. It returns the update's local gid. The errors are the
// store's, as they are (a project that does not resolve, no logged-in user, a status type
// that is not one of Asana's, an empty body), and nothing is written after one.
func (s *Status) Create(ctx context.Context, in StatusInput) (string, error) {
	gid, err := s.Store.ResolveProject(ctx, in.Project)
	if err != nil {
		return "", err
	}
	in.Project = gid
	return s.CreateHere(ctx, in)
}

// CreateHere posts the status update to the project the user already is in, as the TUI
// does: Project is its gid and is not resolved by name. Otherwise it is Create.
func (s *Status) CreateHere(ctx context.Context, in StatusInput) (string, error) {
	me, err := s.Store.Me(ctx)
	if err != nil {
		return "", err
	}
	html, plain := s.Text.StatusUpdate(in.Text)
	return s.Store.CreateStatusLocal(ctx, in.Project, in.Status, in.Title, plain, html, me, s.Clock())
}
