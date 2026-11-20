package outbox

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// The writing side of the payloads (#89): SetPayload, EditPayload, MergeFields,
// CheckFields and PayloadFields.

// sameStored reports whether payload m is stored as the JSON object want.
func sameStored(t *testing.T, m map[string]any, want string) {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := stored(t, string(b)); !reflect.DeepEqual(got, stored(t, want)) {
		t.Errorf("payload = %s, want %s", b, want)
	}
}

func TestSetPayload(t *testing.T) {
	op := Op{Kind: KindMoveTask, Payload: map[string]any{"project": "old", "stale": 1}}
	if err := SetPayload(&op, MoveTask{Project: "p1", Section: "s1", InsertAfter: "t2"}); err != nil {
		t.Fatal(err)
	}
	// the whole payload is replaced: "" omitted where the struct says so, written where not
	sameStored(t, op.Payload, `{"project":"p1","section":"s1","section_name":"","insert_after":"t2"}`)

	// another kind's struct, or an unknown kind: an error, the payload left as it was
	if err := SetPayload(&op, AddTag{Tag: "t"}); err == nil {
		t.Error("SetPayload with another kind's struct: no error")
	}
	unknown := Op{Kind: "from_a_newer_version", Payload: map[string]any{"k": "v"}}
	if err := SetPayload(&unknown, MoveTask{}); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("SetPayload of an unknown kind = %v, want ErrUnknownKind", err)
	}
	sameStored(t, unknown.Payload, `{"k":"v"}`)
}

func TestEditPayload(t *testing.T) {
	for _, c := range []struct {
		name    string
		kind    Kind
		payload string // "" for none (a new entry)
		edit    func(t *testing.T, op *Op) error
		want    string
	}{{
		name: "a new entry gets every key the struct always writes",
		kind: KindMarkNotification,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *MarkNotification) { p.Workspace = "w1" })
		},
		want: `{"workspace":"w1","name":"","read":false}`,
	}, {
		name:    "only what changes is written: the rest stays as stored",
		kind:    KindMarkNotification,
		payload: `{"workspace":"w1","name":"通知","read":false}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *MarkNotification) { p.Read = true })
		},
		want: `{"workspace":"w1","name":"通知","read":true}`,
	}, {
		name:    "a field set to its omitted zero leaves the payload",
		kind:    KindMoveTask,
		payload: `{"project":"p1","section":"s1","section_name":"a","insert_before":"t1"}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *MoveTask) { p.Section, p.InsertBefore, p.InsertAfter = "s2", "", "t3" })
		},
		want: `{"project":"p1","section":"s2","section_name":"a","insert_after":"t3"}`,
	}, {
		name:    "a nil pointer leaves the payload, a pointer to \"\" stays",
		kind:    KindCreateTask,
		payload: `{"workspace":"w1","name":"t","section":"local:s","due_on":""}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *CreateTask) { p.Section = nil })
		},
		want: `{"workspace":"w1","name":"t","due_on":""}`,
	}, {
		name:    "a key the struct does not know (a newer version's) stays: the entry is held with the change",
		kind:    KindCreateTask,
		payload: `{"workspace":"w1","name":"t","from_newer":{"x":1}}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *CreateTask) { s := "s1"; p.Section = &s })
		},
		want: `{"workspace":"w1","name":"t","from_newer":{"x":1},"section":"s1"}`,
	}, {
		name:    "a value of another type left alone stays as stored",
		kind:    KindAddComment,
		payload: `{"text":"a","html_text":5,"story_gid":"local:s"}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *AddComment) { p.Text = "b" })
		},
		want: `{"text":"b","html_text":5,"story_gid":"local:s"}`,
	}, {
		name:    "a value of another type changed is replaced",
		kind:    KindAddComment,
		payload: `{"text":"a","html_text":5,"story_gid":"local:s"}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *AddComment) { p.HTMLText = "<body>b</body>" })
		},
		want: `{"text":"a","html_text":"<body>b</body>","story_gid":"local:s"}`,
	}, {
		name:    "a map field changed in place",
		kind:    KindCreateTask,
		payload: `{"workspace":"w1","name":"t","custom_fields":{"cf1":"o1","cf2":2}}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *CreateTask) { delete(p.CustomFields, "cf1"); p.CustomFields["cf3"] = "x" })
		},
		want: `{"workspace":"w1","name":"t","custom_fields":{"cf2":2,"cf3":"x"}}`,
	}, {
		name:    "the last value of a map field taken out: the key goes",
		kind:    KindCreateTask,
		payload: `{"workspace":"w1","name":"t","custom_fields":{"cf1":"o1"}}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *CreateTask) { delete(p.CustomFields, "cf1") })
		},
		want: `{"workspace":"w1","name":"t"}`,
	}, {
		name:    "no change, no change",
		kind:    KindDeleteTag,
		payload: `{"name":"n","color":"","workspace":"","tasks":null}`,
		edit: func(t *testing.T, op *Op) error {
			return EditPayload(op, func(p *DeleteTag) {})
		},
		want: `{"name":"n","color":"","workspace":"","tasks":null}`,
	}} {
		t.Run(c.name, func(t *testing.T) {
			op := Op{Kind: c.kind}
			var before string
			if c.payload != "" {
				op.Payload = stored(t, c.payload)
				before = c.payload
			}
			orig := op.Payload
			if err := c.edit(t, &op); err != nil {
				t.Fatal(err)
			}
			sameStored(t, op.Payload, c.want)
			if before != "" {
				sameStored(t, orig, before) // the map it had is not changed: op gets a new one
			}
		})
	}
}

func TestEditPayloadErrors(t *testing.T) {
	op := Op{Kind: KindMoveTask, Payload: map[string]any{"project": "p1"}}
	called := false
	if err := EditPayload(&op, func(p *AddTag) { called = true }); err == nil {
		t.Error("EditPayload with another kind's struct: no error")
	}
	unknown := Op{Kind: "from_a_newer_version", Payload: map[string]any{"k": "v"}}
	if err := EditPayload(&unknown, func(p *MoveTask) { called = true }); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("EditPayload of an unknown kind = %v, want ErrUnknownKind", err)
	}
	if called {
		t.Error("the edit was called")
	}
	sameStored(t, op.Payload, `{"project":"p1"}`)
	sameStored(t, unknown.Payload, `{"k":"v"}`)

	// a value that does not encode (a NaN custom field value): an error, nothing written
	cf := Op{Kind: KindSetCustomField, Payload: stored(t, `{"field":"f","field_name":"","type":"number","value":1,"display":"1"}`)}
	nan := 0.0
	if err := EditPayload(&cf, func(p *SetCustomField) { p.Value = nan / nan }); err == nil {
		t.Error("EditPayload to NaN: no error")
	}
	sameStored(t, cf.Payload, `{"field":"f","field_name":"","type":"number","value":1,"display":"1"}`)
}

func TestCheckFields(t *testing.T) {
	for _, c := range []struct {
		kind   Kind
		fields string
		ok     bool
	}{
		{KindUpdateTask, `{"name":"n","completed":true,"due_on":"","assignee":null}`, true},
		{KindCreateTask, `{"name":"n","start_on":"2026-11-01","resource_subtype":"milestone"}`, true},
		{KindUpdateProject, `{"color":"","owner":"u1"}`, true},
		{KindUpdateTask, `{"nmae":"typo"}`, false},
		{KindUpdateTask, `{"completed":"yes"}`, false},
		{KindUpdateProject, `{"name":"not a field of an update_project"}`, false},
	} {
		err := CheckFields(c.kind, stored(t, c.fields))
		if (err == nil) != c.ok {
			t.Errorf("CheckFields(%s, %s) = %v, want ok %v", c.kind, c.fields, err, c.ok)
		}
		var pe *PayloadError
		if err != nil && !errors.As(err, &pe) {
			t.Errorf("CheckFields(%s, %s) = %v, want a *PayloadError", c.kind, c.fields, err)
		}
	}
	if err := CheckFields("from_a_newer_version", nil); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("CheckFields of an unknown kind = %v, want ErrUnknownKind", err)
	}
}

func TestMergeFields(t *testing.T) {
	op := Op{Kind: KindCreateTask, Payload: stored(t, `{"workspace":"w1","name":"t","from_newer":1}`)}
	if err := MergeFields(&op, map[string]any{"name": "u", "due_on": "", "completed": true}); err != nil {
		t.Fatal(err)
	}
	sameStored(t, op.Payload, `{"workspace":"w1","name":"u","from_newer":1,"due_on":"","completed":true}`)

	// a field the create does not have, or a value of another type: nothing merged
	for _, bad := range []map[string]any{{"nmae": "typo", "name": "v"}, {"completed": "yes", "name": "v"}} {
		if err := MergeFields(&op, bad); err == nil {
			t.Errorf("MergeFields(%v): no error", bad)
		}
	}
	sameStored(t, op.Payload, `{"workspace":"w1","name":"u","from_newer":1,"due_on":"","completed":true}`)

	empty := Op{Kind: KindCreateTask}
	if err := MergeFields(&empty, map[string]any{"name": "n"}); err != nil {
		t.Fatal(err)
	}
	sameStored(t, empty.Payload, `{"name":"n"}`)
}

func TestPayloadFields(t *testing.T) {
	op := Op{Kind: KindUpdateTask, Payload: stored(t, `{"name":"n","due_on":"","from_newer":1}`)}
	// what reads, as stored: a key it does not know left out
	sameStored(t, PayloadFields(op), `{"name":"n","due_on":""}`)
	// a value of another type reads as its zero value (as BestEffortPayload)
	op = Op{Kind: KindAddFollower, Payload: stored(t, `{"user":5,"user_name":"n"}`)}
	sameStored(t, PayloadFields(op), `{"user":"","user_name":"n"}`)
	if f := PayloadFields(Op{Kind: "from_a_newer_version", Payload: map[string]any{"k": 1}}); f != nil {
		t.Errorf("PayloadFields of an unknown kind = %v, want nil", f)
	}
	sameStored(t, PayloadFields(Op{Kind: KindRenameSection, Payload: stored(t, `{"name":"s"}`)}), `{"name":"s"}`)
}
