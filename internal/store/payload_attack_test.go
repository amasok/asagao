package store

import (
	"context"
	"strings"
	"testing"

	"github.com/amasok/asagao/internal/domain/rich"
)

// Attack tests for the writing side of the payloads (#89): entries holding keys or values
// this version does not read are edited without losing them, and every path keeps the
// stored form.

func insertRawOp(t *testing.T, s *Store, kind Kind, gid, payload, base string) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO outbox(kind, task_gid, payload, base, base_modified_at, state, conflict, created_at)
		VALUES(?, ?, ?, ?, '', 'pending', '{}', '')`, kind, gid, payload, base)
	must(t, err)
	id, err := res.LastInsertId()
	must(t, err)
	return id
}

func TestAttackWithdrawStartOnKeepsFutureKeys(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	id := insertRawOp(t, s, OpUpdateTask, "t1", `{"name":"x","start_on":"2026-12-01","from_future":{"a":[1]}}`, `{"start_on":"2026-11-01"}`)
	rest, d, err := s.WithdrawStartOn(ctx, id, "plan", testNow)
	must(t, err)
	if rest == nil {
		t.Fatal("rest is nil: the entry with other keys was removed")
	}
	wantStored(t, s, OpUpdateTask, `{"name":"x","from_future":{"a":[1]}}`)
	if got := d.Payload["start_on"]; got != "2026-12-01" || len(d.Payload) != 1 {
		t.Errorf("discard payload %v", d.Payload)
	}
}

func TestAttackWithdrawStartOnCreateAndOnlyFutureKey(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	id := insertRawOp(t, s, OpCreateTask, "local:1", `{"workspace":"w1","name":"n","start_on":"","from_future":1}`, `{}`)
	rest, d, err := s.WithdrawStartOn(ctx, id, "plan", testNow)
	must(t, err)
	if rest == nil || d.Payload["start_on"] != "" {
		t.Fatalf("rest %v discard %v", rest, d.Payload)
	}
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"n","from_future":1}`)
	// update with only start_on and a future key: kept (the future key is not ours to drop)
	id2 := insertRawOp(t, s, OpUpdateTask, "t2", `{"start_on":"2026-12-01","from_future":1}`, `{}`)
	rest, _, err = s.WithdrawStartOn(ctx, id2, "plan", testNow)
	must(t, err)
	if rest == nil {
		t.Error("an update still holding a key of a newer version was deleted")
	}
}

func TestAttackWithdrawStartOnMistypedValue(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	id := insertRawOp(t, s, OpUpdateTask, "t1", `{"name":"x","start_on":5}`, `{}`)
	_, _, err := s.WithdrawStartOn(ctx, id, "plan", testNow)
	must(t, err)
	// the value was not readable: it must not be sent as it is, nor lost silently
	got := storedPayloads(t, s, OpUpdateTask)
	if len(got) == 1 && !strings.Contains(got[0], `"name":"x"`) {
		t.Errorf("name lost: %v", got)
	}
}

func TestAttackWithdrawCustomTypeKeepsFutureKeys(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	id := insertRawOp(t, s, OpUpdateTask, "t1",
		`{"name":"x","custom_type":"ct1","custom_type_status_option":"o1","resource_subtype":"custom","from_future":[1]}`,
		`{"custom_type":"","resource_subtype":"default_task"}`)
	rest, d, err := s.WithdrawCustomType(ctx, id, "no", false, testNow)
	must(t, err)
	if rest == nil {
		t.Fatal("rest nil")
	}
	wantStored(t, s, OpUpdateTask, `{"name":"x","from_future":[1]}`)
	for _, k := range []string{"custom_type", "custom_type_status_option", "resource_subtype"} {
		if _, ok := d.Payload[k]; !ok {
			t.Errorf("discard payload lacks %s: %v", k, d.Payload)
		}
	}
	if len(d.Payload) != 3 {
		t.Errorf("discard payload %v", d.Payload)
	}
}

func TestAttackWithdrawCustomTypeNotCustomSubtype(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	id := insertRawOp(t, s, OpUpdateTask, "t1", `{"name":"x","custom_type":"ct1","resource_subtype":"milestone"}`, `{}`)
	_, d, err := s.WithdrawCustomType(ctx, id, "no", false, testNow)
	must(t, err)
	wantStored(t, s, OpUpdateTask, `{"name":"x","resource_subtype":"milestone"}`)
	if _, ok := d.Payload["resource_subtype"]; ok {
		t.Errorf("milestone subtype withdrawn: %v", d.Payload)
	}
}

func TestAttackSetSectionsExistingKind(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	must(t, s.RenameSectionLocal(ctx, "s1", "別名", testNow))
	id := storedOpID(t, s, OpRenameSection)
	if err := s.SetSectionsExisting(ctx, id, []string{"s9"}); err == nil {
		t.Error("another kind's entry took existing")
	}
	wantStored(t, s, OpRenameSection, `{"name":"別名"}`)
	if err := s.SetSectionsExisting(ctx, 99999, nil); err == nil {
		t.Error("missing entry: no error")
	}
	// future keys survive; nil clears existing again
	cid := insertRawOp(t, s, OpCreateSection, "local:s", `{"project":"p1","name":"n","from_future":true}`, `{}`)
	must(t, s.SetSectionsExisting(ctx, cid, []string{"a"}))
	wantStored(t, s, OpCreateSection, `{"project":"p1","name":"n","from_future":true,"existing":["a"]}`)
	must(t, s.SetSectionsExisting(ctx, cid, []string{}))
	wantStored(t, s, OpCreateSection, `{"project":"p1","name":"n","from_future":true,"existing":[]}`)
}

func TestAttackFitQueuedRichText(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertRawOp(t, s, OpAddComment, "t1", `{"text":"a","html_text":"<body>a</body>","story_gid":"local:c","from_future":1}`, `{}`)
	insertRawOp(t, s, OpCreateStatus, "p1", `{"status":"local:s","status_type":"on_track","title":"T","text":"b","html_text":"<body>b</body>"}`, `{}`)
	n, err := s.FitQueuedRichText(ctx, func(_ rich.Target, h string) (string, string, bool) {
		return strings.ReplaceAll(h, "body", "doc"), "plain", true
	})
	must(t, err)
	if n != 2 {
		t.Fatalf("fixed %d", n)
	}
	wantStored(t, s, OpAddComment, `{"text":"plain","html_text":"<doc>a</doc>","story_gid":"local:c","from_future":1}`)
	wantStored(t, s, OpCreateStatus, `{"status":"local:s","status_type":"on_track","title":"T","text":"plain","html_text":"<doc>b</doc>"}`)
}

func TestAttackCompleteCreateMove(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	gid, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", SectionGID: "s1", Name: "n", DueOn: "2026-12-01"}, testNow)
	must(t, err)
	id := storedOpID(t, s, OpCreateTask)
	must(t, s.CompleteCreate(ctx, id, gid, "R1", &MoveTarget{Project: "p1", Section: "s2", From: "s1"}))
	wantStored(t, s, OpMoveTask, `{"project":"p1","section":"s2","section_name":"手続き"}`)
	if n := len(storedPayloads(t, s, OpCreateTask)); n != 0 {
		t.Errorf("%d create entries left", n)
	}
}

func TestAttackFutureKeysSurviveEdits(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	insertRawOp(t, s, OpRenameSection, "s1", `{"name":"古い","from_future":{"x":1}}`, `{"name":"家具"}`)
	must(t, s.RenameSectionLocal(ctx, "s1", "新しい", testNow))
	wantStored(t, s, OpRenameSection, `{"name":"新しい","from_future":{"x":1}}`)

	// a mistyped value in a key being edited is replaced; the other keys stay
	gid, err := s.CreateTaskLocal(ctx, NewTask{WorkspaceGID: "w1", Name: "n"}, testNow)
	must(t, err)
	_, err = s.db.Exec(`UPDATE outbox SET payload = ? WHERE kind = 'create_task'`, `{"workspace":"w1","name":"n","project":"p1","section":5,"due_on":["x"]}`)
	must(t, err)
	must(t, s.UpdateTaskLocal(ctx, gid, map[string]any{"name": "m"}, testNow))
	got := storedPayloads(t, s, OpCreateTask)
	if len(got) != 1 || !strings.Contains(got[0], `"name":"m"`) || !strings.Contains(got[0], `"section":5`) || !strings.Contains(got[0], `"due_on":["x"]`) {
		t.Errorf("mistyped values changed: %v", got)
	}
}

// Changes with a key or a value an update_task cannot hold are refused, not queued.
func TestAttackUpdateTaskLocalBadChanges(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, ch := range []map[string]any{{"bogus": 1}, {"name": 5}, {"completed": "yes"}} {
		if err := s.UpdateTaskLocal(ctx, "t1", ch, testNow); err == nil {
			t.Errorf("%v: no error", ch)
		}
	}
	if n := len(storedPayloads(t, s, OpUpdateTask)); n != 0 {
		t.Errorf("%d entries queued by refused edits", n)
	}
}

// Discarding an option created offline rewrites the entries that name it, in the stored
// form: a create_task keeps the other custom fields and its unknown keys, and an
// untouched key is not rewritten.
func TestAttackUndoEnumOptionStoredForm(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	local, _, err := s.AddEnumOptionLocal(ctx, "cf1", "低", testNow)
	must(t, err)
	task, err := s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", Name: "新"}, testNow)
	must(t, err)
	_, err = s.db.Exec(`UPDATE outbox SET payload = ? WHERE kind = 'create_task'`,
		`{"workspace":"w1","name":"新","project":"p1","from_future":[1],"custom_fields":{"cf1":"`+local+`","cf5":"メモ","cf4":["m1","`+local+`"]}}`)
	must(t, err)
	_ = task
	// a set_custom_field of another field, naming the local gid only in a key of a newer version
	insertRawOp(t, s, OpSetCustomField, "t1", `{"field":"cf5","field_name":"メモ","type":"text","value":"x","display":"x","from_future":"`+local+`"}`, `{}`)
	must(t, s.DiscardOp(ctx, storedOpID(t, s, OpCreateEnumOption)))
	wantStored(t, s, OpCreateTask,
		`{"workspace":"w1","name":"新","project":"p1","from_future":[1],"custom_fields":{"cf5":"メモ","cf4":["m1"]}}`)
	wantStored(t, s, OpSetCustomField, `{"field":"cf5","field_name":"メモ","type":"text","value":"x","display":"x","from_future":"`+local+`"}`)
}

// A create_task whose only custom field was the discarded option loses custom_fields.
func TestAttackUndoEnumOptionLastField(t *testing.T) {
	s := setupFieldOps(t)
	ctx := context.Background()
	local, _, err := s.AddEnumOptionLocal(ctx, "cf1", "低", testNow)
	must(t, err)
	_, err = s.CreateTaskLocal(ctx, NewTask{ProjectGID: "p1", Name: "新"}, testNow)
	must(t, err)
	_, err = s.db.Exec(`UPDATE outbox SET payload = ? WHERE kind = 'create_task'`,
		`{"workspace":"w1","name":"新","project":"p1","custom_fields":{"cf1":"`+local+`"}}`)
	must(t, err)
	must(t, s.DiscardOp(ctx, storedOpID(t, s, OpCreateEnumOption)))
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"新","project":"p1"}`)
}

// Forgetting a section that an add_project / create_task names takes the section out and
// nothing else.
func TestAttackForgetSectionAddProject(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	ls, err := s.CreateSectionLocal(ctx, "p1", "消す", "", "", testNow)
	must(t, err)
	insertRawOp(t, s, OpAddProject, "t1", `{"project":"p1","project_name":"P","section":"`+ls+`","from_future":1}`, `{}`)
	insertRawOp(t, s, OpCreateTask, "local:zz", `{"workspace":"w1","name":"n","project":"p1","section":"`+ls+`","due_on":""}`, `{}`)
	must(t, s.DeleteSectionLocal(ctx, ls, testNow))
	wantStored(t, s, OpAddProject, `{"project":"p1","project_name":"P","from_future":1}`)
	wantStored(t, s, OpCreateTask, `{"workspace":"w1","name":"n","project":"p1","due_on":""}`)
}

// "Keep mine" on a brief entry replaces the payload with the whole brief; a delete stays.
func TestAttackBriefKeepLocalDelete(t *testing.T) {
	s, ctx := briefStore(t, asanaBrief)
	must(t, s.UpdateProjectBriefLocal(ctx, "p1", BriefEdit{Delete: true}, testNow))
	op := briefEntries(t, s)[0]
	must(t, s.MarkOpConflict(ctx, op.ID, map[string]any{"state": BriefNone}))
	must(t, s.ResolveKeepLocal(ctx, op.ID))
	wantStored(t, s, OpUpdateBrief, `{"delete":true}`)
}
