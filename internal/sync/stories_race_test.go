package sync

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana/model"
	"github.com/amasok/asagao/internal/store"
)

// storiesRaceAPI answers FetchDetails; while the stories are being read, a comment is sent
// (by another process) and the clock moves on.
type storiesRaceAPI struct {
	AsanaAPI
	during func()
	list   []model.Story
}

func (a *storiesRaceAPI) TaskDetail(ctx context.Context, gid string) (*model.Task, error) {
	return &model.Task{GID: gid, Name: "A"}, nil
}
func (a *storiesRaceAPI) Subtasks(ctx context.Context, gid string) ([]model.Task, error) {
	return nil, nil
}
func (a *storiesRaceAPI) Attachments(ctx context.Context, gid string) ([]model.Attachment, error) {
	return nil, nil
}
func (a *storiesRaceAPI) Stories(ctx context.Context, gid string) ([]model.Story, error) {
	a.during()
	return a.list, nil
}

// #40: the list read before a comment was sent does not hide that comment.
func TestFetchDetailsKeepsACommentSentWhileReadingStories(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.UpsertTasks(ctx, []model.Task{{GID: "t1", Name: "A"}}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	clock := t0
	local, err := st.AddCommentLocal(ctx, "t1", "送る", store.UserRef{GID: "me"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	api := &storiesRaceAPI{during: func() {
		if err := st.ReplaceLocalStory(ctx, local, "t1", model.Story{GID: "c9", Type: "comment", Text: "送る", CreatedAt: t0.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		clock = t0.Add(10 * time.Second) // the list came back later
	}}
	s := &Syncer{API: api, Store: st, now: func() time.Time { return clock }}
	if err := s.FetchDetails(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	d, err := st.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stories) != 1 || d.Stories[0].GID != "c9" {
		t.Fatalf("stories %+v", d.Stories)
	}
}
