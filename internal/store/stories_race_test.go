package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	asana "github.com/amasok/asagao/internal/asana/model"
)

// #40: a refresh that read the comments before ours was sent must not hide it.
func TestReplaceStoriesKeepsCommentsSentDuringTheFetch(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "t1", Name: "A"}}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	old := asana.Story{GID: "c1", Type: "comment", Text: "前からある", CreatedAt: start.Add(-time.Hour)}
	gone := asana.Story{GID: "c0", Type: "comment", Text: "Asana で消えた", CreatedAt: start.Add(-2 * time.Hour)}
	if err := s.ReplaceStories(ctx, "t1", []asana.Story{gone, old}, start.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	// queued now, sent while the refresh below was reading the list
	gid, err := s.AddCommentLocal(ctx, "t1", "いま書いた", UserRef{GID: "me"}, start)
	if err != nil {
		t.Fatal(err)
	}
	sent := asana.Story{GID: "c2", Type: "comment", Text: "いま書いた", CreatedAt: start.Add(time.Second)}
	if err := s.ReplaceLocalStory(ctx, gid, "t1", sent); err != nil {
		t.Fatal(err)
	}
	queued, err := s.AddCommentLocal(ctx, "t1", "まだ送っていない", UserRef{GID: "me"}, start)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStories(ctx, "t1", []asana.Story{old}, start); err != nil {
		t.Fatal(err)
	}
	if got := storyGIDs(t, s, "t1"); !equal(got, []string{"c1", queued, "c2"}) {
		t.Fatalf("after a stale refresh: %v", got)
	}
	// the next refresh has it (and the text Asana has now)
	sent.Text = "直した"
	if err := s.ReplaceStories(ctx, "t1", []asana.Story{old, sent}, start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Stories) != 3 || d.Stories[2].Text != "直した" {
		t.Fatalf("stories %+v", d.Stories)
	}
	if n := countFTS(t, s, "直した"); n != 1 {
		t.Errorf("full-text index has %d rows for the new text", n)
	}
	// a refresh started after it was deleted in Asana drops it
	if err := s.ReplaceStories(ctx, "t1", []asana.Story{old}, start.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := storyGIDs(t, s, "t1"); !equal(got, []string{"c1", queued}) {
		t.Fatalf("after a later refresh: %v", got)
	}
}

func storyGIDs(t *testing.T, s *Store, task string) []string {
	t.Helper()
	d, err := s.GetTask(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, st := range d.Stories {
		out = append(out, st.GID)
	}
	return out
}

func countFTS(t *testing.T, s *Store, word string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM stories_fts WHERE stories_fts MATCH ?", `"`+word+`"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
