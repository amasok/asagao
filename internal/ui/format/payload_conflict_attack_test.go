package format

import (
	"reflect"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
	"github.com/amasok/asagao/internal/readmodel"
)

func TestAttackConflictLinesSameAsMapReading(t *testing.T) {
	cases := []struct {
		op   outbox.Op
		want []string
	}{
		{outbox.Op{Kind: outbox.KindUpdateTask, Payload: map[string]any{"name": "こちら", "due_on": "", "completed": true},
			Conflict: map[string]any{"name": "Asana", "due_on": "2026-12-01", "completed": false}},
			[]string{"完了: こちら=true / Asana=false", "期日: こちら= / Asana=2026-12-01", "名前: こちら=こちら / Asana=Asana"}},
		{outbox.Op{Kind: outbox.KindRenameSection, Payload: map[string]any{"name": "新"}, Conflict: map[string]any{"name": "別"}},
			[]string{"名前: こちら=新 / Asana=別"}},
		{outbox.Op{Kind: outbox.KindUpdateTag, Payload: map[string]any{"color": "red"}, Conflict: map[string]any{"color": "blue", "name": "n"}},
			nil},
		{outbox.Op{Kind: outbox.KindSetCustomField, Payload: map[string]any{"field_name": "優先度", "value": "o1", "display": "高"},
			Conflict: map[string]any{"value": nil}},
			[]string{"優先度: こちら=高 / Asana=（空）"}},
		{outbox.Op{Kind: outbox.KindSetCustomField, Payload: map[string]any{"field_name": "見積", "value": 3.5},
			Conflict: map[string]any{"value": 4.0, "display": "4"}},
			[]string{"見積: こちら=3.5 / Asana=4"}},
	}
	for i, c := range cases {
		got := ConflictLines(c.op)
		if c.want == nil {
			if len(got) != 2 {
				t.Errorf("%d: %q", i, got)
			}
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%d: got %q want %q", i, got, c.want)
		}
	}
}

func TestAttackConflictLinesUnreadable(t *testing.T) {
	for _, k := range outbox.AllKinds() {
		op := outbox.Op{Kind: k, Payload: map[string]any{"name": 5.0, "value": []any{1.0}, "title": false, "from_future": 1.0, "delete": "x", "field_name": 3.0},
			Conflict: map[string]any{"name": "a", "value": "b", "title": "t", "text": "x", "state": "available", "due_on": nil}}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v", k, r)
				}
			}()
			_ = ConflictLines(op)
			_ = BriefConflictLines(op)
			_ = DiscardDetails(readmodel.Discard{Kind: k, Payload: op.Payload}, func(h string) string { return h })
		}()
	}
}

func TestAttackBriefConflictLines(t *testing.T) {
	str := func(s string) *string { return &s }
	_ = str
	avail := map[string]any{"state": readmodel.BriefAvailable, "title": "T", "text": "body"}
	// only the title changed: one line
	got := BriefConflictLines(outbox.Op{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"title": "新"}, Conflict: avail})
	if len(got) != 1 || got[0] != "ブリーフのタイトル: こちら=新 / Asana=T" {
		t.Errorf("%q", got)
	}
	// delete
	got = BriefConflictLines(outbox.Op{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"delete": true}, Conflict: avail})
	if len(got) != 2 || got[0] != "ブリーフのタイトル: こちら=（削除） / Asana=T" {
		t.Errorf("%q", got)
	}
	// same as Asana's: the fallback
	got = BriefConflictLines(outbox.Op{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"title": "T", "text": "body"}, Conflict: avail})
	if len(got) != 1 || got[0] != "ブリーフ: Asana でも変更されています" {
		t.Errorf("%q", got)
	}
	// an empty-string title is a change (cleared), not an absent one
	got = BriefConflictLines(outbox.Op{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"title": ""}, Conflict: avail})
	if len(got) != 1 || got[0] != "ブリーフのタイトル: こちら= / Asana=T" {
		t.Errorf("%q", got)
	}
	// Asana's brief gone
	got = BriefConflictLines(outbox.Op{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"title": "新"}, Conflict: map[string]any{"state": "none"}})
	if len(got) != 1 {
		t.Errorf("%q", got)
	}
}

func TestAttackDiscardDetails(t *testing.T) {
	md := func(h string) string { return "MD:" + h }
	got := DiscardDetails(readmodel.Discard{Kind: outbox.KindUpdateTask, Payload: map[string]any{"name": "n\x1b[31m", "notes": "p", "html_notes": "<b>"}}, md)
	if len(got) != 2 || got[0].Text != "n[31m" && got[0].Text != "n" || got[1].Text != "MD:<b>" {
		t.Errorf("%+v", got)
	}
	got = DiscardDetails(readmodel.Discard{Kind: outbox.KindUpdateProject, Payload: map[string]any{"notes": "p"}}, md)
	if len(got) != 1 || got[0].Text != "p" {
		t.Errorf("%+v", got)
	}
	// html that converts to nothing falls back to the plain text
	got = DiscardDetails(readmodel.Discard{Kind: outbox.KindAddComment, Payload: map[string]any{"text": "plain", "html_text": "<x/>"}}, func(string) string { return " " })
	if len(got) != 1 || got[0].Text != "plain" {
		t.Errorf("%+v", got)
	}
	got = DiscardDetails(readmodel.Discard{Kind: outbox.KindUpdateProjectBrief, Payload: map[string]any{"title": "t", "text": "x"}}, md)
	if len(got) != 2 {
		t.Errorf("%+v", got)
	}
	if got := DiscardDetails(readmodel.Discard{Kind: outbox.KindDeleteTask, Payload: map[string]any{"name": "n"}}, md); len(got) != 0 {
		t.Errorf("%+v", got)
	}
}

// A conflicting field the payload struct does not read (a kind or a key of a newer
// version) is shown as stored, not as <nil>.
func TestConflictLinesShowWhatTheStructDoesNotRead(t *testing.T) {
	for _, op := range []outbox.Op{
		{Kind: "from_a_newer_version", Payload: map[string]any{"name": "こちら"}, Conflict: map[string]any{"name": "Asana"}},
		{Kind: outbox.KindUpdateTask, Payload: map[string]any{"name": "こちら", "from_newer": "こちら"},
			Conflict: map[string]any{"from_newer": "Asana"}},
	} {
		got := ConflictLines(op)
		if len(got) != 1 || !strings.Contains(got[0], "こちら=こちら / Asana=Asana") {
			t.Errorf("ConflictLines(%s) = %q, want the stored value", op.Kind, got)
		}
	}
}
