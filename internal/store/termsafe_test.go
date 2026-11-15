package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asana-tui/internal/asana"
	"github.com/amasok/asana-tui/internal/termsafe"
)

const evil = "\x1b]52;c;ZXZpbA==\x07\x1b_Ga=T;AAAA\x1b\\\u009b2J"

// Text is read out of the store without terminal control characters, however it got in.
func TestStoreReadsAreTerminalSafe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "e1", Name: "悪い" + evil, Notes: "本文" + evil + "\n2行目",
		Assignee: &asana.Ref{GID: "me", Name: "Me" + evil}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceStories(ctx, "e1", []asana.Story{{GID: "c9", Type: "comment", Text: "コメント" + evil,
		CreatedBy: &asana.Ref{GID: "u9", Name: "誰か" + evil}}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetTask(ctx, "e1")
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{d.Name, d.Notes, d.Assignee.Name}
	for _, st := range d.Stories {
		texts = append(texts, st.Text)
		if st.CreatedBy != nil {
			texts = append(texts, st.CreatedBy.Name)
		}
	}
	ts, err := s.ListTasks(ctx, TaskFilter{Query: "悪い"})
	if err != nil || len(ts) != 1 {
		t.Fatalf("ListTasks: %v %v", ts, err)
	}
	texts = append(texts, ts[0].Name)
	for _, x := range texts {
		if !termsafe.Safe(x) {
			t.Errorf("control characters in %q", x)
		}
	}
	if d.Notes != "本文]52;c;ZXZpbA==_Ga=T;AAAA\\2J\n2行目" {
		t.Errorf("notes = %q", d.Notes)
	}

	// JSON kept in TEXT columns: escapes are removed too, so decoding it is safe.
	if err := s.SetSyncState(ctx, "k", `{"name":"x\u001b]52;c;Zm9v\u0007","n":"a\nb"}`); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.SyncState(ctx, "k"); strings.Contains(v, `\u001b`) || strings.Contains(v, `\u0007`) || !strings.Contains(v, `x]52;c;Zm9v`) {
		t.Errorf("json = %q", v)
	}
}
