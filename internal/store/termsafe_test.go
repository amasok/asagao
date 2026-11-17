package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/asana"
	"github.com/amasok/asagao/internal/termsafe"
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
}

// Plain text that only mentions an escape (`\u001b[31m` written as characters) is not JSON
// and must come back as it is: editing and saving it would otherwise lose it on Asana.
func TestStoreKeepsLiteralEscapeTextInPlainText(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	notes := "赤字は `\\u001b[31m` で始める\n{\"memo\": \"\\u001b\"} も文字のまま\r"
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "e2", Name: `\u001b[31m 赤`, Notes: notes}}); err != nil {
		t.Fatal(err)
	}
	d, err := s.GetTask(ctx, "e2")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != `\u001b[31m 赤` {
		t.Errorf("name = %q", d.Name)
	}
	if want := strings.TrimSuffix(notes, "\r"); d.Notes != want { // only the raw \r goes
		t.Errorf("notes = %q, want %q", d.Notes, want)
	}
}

// Notes that are JSON as a whole are text all the same: an escape written in them stays.
func TestStoreKeepsEscapesInJSONLikeNotes(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for i, notes := range []string{`"a\u001b"`, `{"color":"\u001b[31m"}`, `["\u009b"]`} {
		gid := fmt.Sprintf("j%d", i)
		if err := s.UpsertTasks(ctx, []asana.Task{{GID: gid, Name: notes, Notes: notes}}); err != nil {
			t.Fatal(err)
		}
		d, err := s.GetTask(ctx, gid)
		if err != nil {
			t.Fatal(err)
		}
		if d.Name != notes || d.Notes != notes {
			t.Errorf("got name %q notes %q, want %q", d.Name, d.Notes, notes)
		}
	}
}

// JSON columns are decoded with termsafe.Unmarshal: a custom field value that Asana sent
// with ESC in it (stored as the JSON string "...\u001b...") comes out without it.
func TestStoreCustomFieldValueIsTerminalSafe(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	val := "値" + evil
	if err := s.UpsertTasks(ctx, []asana.Task{{GID: "f1", Name: "フィールド",
		CustomFields: []asana.CustomField{{GID: "cf", Name: "メモ", ResourceSubtype: "text", TextValue: &val}}}}); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM task_custom_fields WHERE task_gid = 'f1'").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, `\u001b`) {
		t.Fatalf("the column should hold the escape (that is the case under test): %q", stored)
	}
	d, err := s.GetTask(ctx, "f1")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.CustomFields) != 1 {
		t.Fatalf("fields = %+v", d.CustomFields)
	}
	f := d.CustomFields[0]
	if v, _ := f.Value.(string); v != "値]52;c;ZXZpbA==_Ga=T;AAAA\\2J" || !termsafe.Safe(f.Display) {
		t.Errorf("value %q display %q", f.Value, f.Display)
	}
}
