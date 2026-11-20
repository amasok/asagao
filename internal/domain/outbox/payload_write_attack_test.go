package outbox

import "testing"

func TestAttackEditPayloadMistypedPointer(t *testing.T) {
	// a pointer field holding a value of another type: left as stored when f does not touch
	// it, replaced when f sets it, taken out when f clears it; other keys stay
	mk := func() *Op {
		return &Op{Kind: KindCreateTask, Payload: map[string]any{"workspace": "w", "name": "n", "section": float64(5), "from_future": []any{1.0}}}
	}
	op := mk()
	if err := EditPayload(op, func(p *CreateTask) { p.Name = "m" }); err != nil {
		t.Fatal(err)
	}
	sameStored(t, op.Payload, `{"workspace":"w","name":"m","section":5,"from_future":[1]}`)
	op = mk()
	if err := EditPayload(op, func(p *CreateTask) { s := "s2"; p.Section = &s }); err != nil {
		t.Fatal(err)
	}
	sameStored(t, op.Payload, `{"workspace":"w","name":"n","section":"s2","from_future":[1]}`)
	op = mk()
	if err := EditPayload(op, func(p *CreateTask) { p.Section = nil }); err != nil {
		t.Fatal(err)
	}
	sameStored(t, op.Payload, `{"workspace":"w","name":"n","from_future":[1]}`)
}

func TestAttackEditPayloadMistypedPlainField(t *testing.T) {
	op := &Op{Kind: KindRenameSection, Payload: map[string]any{"name": float64(5), "x": "y"}}
	if err := EditPayload(op, func(p *RenameSection) {}); err != nil {
		t.Fatal(err)
	}
	sameStored(t, op.Payload, `{"name":5,"x":"y"}`)
	if err := EditPayload(op, func(p *RenameSection) { p.Name = "ok" }); err != nil {
		t.Fatal(err)
	}
	sameStored(t, op.Payload, `{"name":"ok","x":"y"}`)
}

func TestAttackEditPayloadEmptyAndNil(t *testing.T) {
	for _, pl := range []map[string]any{nil, {}} {
		op := &Op{Kind: KindUpdateTask, Payload: pl}
		if err := EditPayload(op, func(p *UpdateTask) { s := ""; p.DueOn = &s }); err != nil {
			t.Fatal(err)
		}
		sameStored(t, op.Payload, `{"due_on":""}`)
	}
	// f that changes nothing on a new entry: no stray keys for an all-omitempty kind
	op := &Op{Kind: KindUpdateTask}
	if err := EditPayload(op, func(p *UpdateTask) {}); err != nil {
		t.Fatal(err)
	}
	sameStored(t, op.Payload, `{}`)
}

func TestAttackEditPayloadDoesNotAliasInput(t *testing.T) {
	orig := map[string]any{"name": "a"}
	op := &Op{Kind: KindRenameSection, Payload: orig}
	if err := EditPayload(op, func(p *RenameSection) { p.Name = "b" }); err != nil {
		t.Fatal(err)
	}
	if orig["name"] != "a" {
		t.Errorf("input map mutated: %v", orig)
	}
}

func TestAttackEditPayloadWrongKindAndUnknown(t *testing.T) {
	op := &Op{Kind: KindRenameSection, Payload: map[string]any{"name": "a"}}
	if err := EditPayload(op, func(p *MoveTask) { p.Project = "x" }); err == nil {
		t.Error("wrong struct: no error")
	}
	sameStored(t, op.Payload, `{"name":"a"}`)
	if err := MergeFields(&Op{Kind: "zzz"}, map[string]any{"a": 1}); err == nil {
		t.Error("unknown kind merged")
	}
	if err := CheckFields(KindUpdateTask, map[string]any{"name": nil, "due_on": nil}); err != nil {
		t.Errorf("null values: %v", err)
	}
}
