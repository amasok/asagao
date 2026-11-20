package outbox

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

// storedPayloads are payloads of every kind as the store writes them (store/*.go), in
// each of the forms an entry may have: the keys written only sometimes, "" and absent
// where they mean different things, and the keys added or taken out afterwards.
var storedPayloads = map[Kind][]string{
	KindUpdateTask: {
		`{"completed":true}`,
		`{"completed":false,"liked":false}`,
		`{"name":"牛乳を買う","notes":"メモ","html_notes":"<body>メモ</body>"}`,
		`{"due_on":"","start_on":"","assignee":""}`,
		`{"due_on":"2026-11-20","start_on":"2026-11-01","assignee":"1200"}`,
		`{"resource_subtype":"custom","custom_type":"ct1","custom_type_status_option":"st1"}`,
		`{"resource_subtype":"milestone","approval_status":"approved"}`,
		`{"html_notes":"<body>だけ</body>"}`,
	},
	KindCreateTask: {
		`{"workspace":"w1","name":"新しいタスク"}`,
		`{"workspace":"","name":"子","parent":"local:p"}`,
		`{"workspace":"w1","name":"t","project":"p1","section":"s1","notes":"n","html_notes":"<body>n</body>","due_on":"2026-11-20","start_on":"2026-11-01","assignee":"u1"}`,
		`{"workspace":"w1","name":"t","section":""}`,
		`{"workspace":"w1","name":"t","completed":true,"liked":false,"resource_subtype":"milestone","approval_status":"pending","due_on":"","start_on":"","assignee":""}`,
		`{"workspace":"w1","name":"t","custom_fields":{"cf1":"opt1","cf2":3.5,"cf3":["u1","u2"],"cf4":{"date":"2026-11-20"},"cf5":"text"}}`,
		`{"workspace":"w1","name":"t","custom_type":"ct1","custom_type_status_option":"st1"}`,
	},
	KindDuplicateTask: {`{"source":"123","name":"t のコピー"}`},
	KindAddComment: {
		`{"text":"こんにちは","story_gid":"local:s1"}`,
		`{"text":"こんにちは","story_gid":"local:s1","html_text":"<body>こんにちは</body>"}`,
		`{"text":"古い形"}`, // without story_gid (the undo allows it)
	},
	KindMoveTask: {
		`{"project":"p1","section":"s1","section_name":"作業中"}`,
		`{"project":"p1","section":"local:s1","section_name":"","insert_before":"t2"}`,
		`{"project":"p1","section":"s1","section_name":"作業中","insert_after":"local:t3"}`,
	},
	KindRenameSection: {`{"name":"新しい名前"}`},
	KindCreateSection: {
		`{"project":"p1","name":"新セクション"}`,
		`{"project":"p1","name":"新セクション","before":"s2"}`,
		`{"project":"p1","name":"新セクション","after":"local:s3","existing":[]}`,
		`{"project":"p1","name":"新セクション","existing":["s4","s5"]}`,
	},
	KindDeleteSection: {`{"project":"p1","name":"古いセクション","position":2}`},
	KindInsertSection: {
		`{"project":"p1","name":"s","before":"s2"}`,
		`{"project":"p1","name":"s","after":"s3"}`,
	},
	KindUpdateTag: {
		`{"name":"新しい名前"}`,
		`{"color":""}`,
		`{"name":"x","color":"dark-red"}`,
	},
	KindDeleteTag: {
		`{"name":"タグ","color":"dark-red","workspace":"w1","tasks":["t1","t2"]}`,
		`{"name":"タグ","color":"","workspace":"","tasks":[]}`,
	},
	KindCreateStatus: {
		`{"status":"local:st1","status_type":"on_track","title":"","text":"順調","html_text":""}`,
		`{"status":"local:st1","status_type":"at_risk","title":"週報","text":"遅れ","html_text":"<body>遅れ</body>"}`,
	},
	KindAddTag: {
		`{"tag":"123","tag_name":"重要","workspace":"w1"}`,
		`{"tag":"local:t1","tag_name":"新タグ","workspace":""}`,
	},
	KindRemoveTag:        {`{"tag":"123","tag_name":""}`},
	KindAddFollower:      {`{"user":"u1","user_name":"山田"}`},
	KindRemoveFollower:   {`{"user":"u1","user_name":""}`},
	KindAddProject:       {`{"project":"p1","project_name":"P","section":"s1"}`, `{"project":"p1","project_name":"P","section":""}`, `{"project":"p1","project_name":"P"}`},
	KindRemoveProject:    {`{"project":"p1","project_name":"P","section":""}`},
	KindUploadAttachment: {`{"attachment":"local:a1","name":"図.png","path":"/data/attachments/a1"}`},
	KindDeleteTask:       {`{"name":"消すタスク"}`},
	KindSetParent: {
		`{"parent":"","parent_name":""}`,
		`{"parent":"local:p1","parent_name":"親","insert_before":"t2"}`,
		`{"parent":"p1","parent_name":"親","insert_after":"t3"}`,
	},
	KindAddDependency:    {`{"dependency":"local:d1","dependency_name":"前のタスク"}`},
	KindRemoveDependency: {`{"dependency":"d1","dependency_name":"前のタスク"}`},
	KindSetCustomField: {
		`{"field":"cf1","field_name":"優先度","type":"enum","value":"opt1","display":"高"}`,
		`{"field":"cf1","field_name":"優先度","type":"enum","value":null,"display":""}`,
		`{"field":"cf2","field_name":"見積","type":"number","value":3.5,"display":"3.5"}`,
		`{"field":"cf3","field_name":"担当","type":"people","value":["u1","u2"],"display":"山田, 鈴木"}`,
		`{"field":"cf4","field_name":"締切","type":"date","value":"2026-11-20","display":"2026-11-20"}`,
	},
	KindMarkNotification: {
		`{"workspace":"w1","name":"タスク","read":true}`,
		`{"workspace":"w1","name":"タスク","read":false}`,
	},
	KindUpdateStory: {
		`{"story":"s1","text":"直した","html_text":""}`,
		`{"story":"local:s1","text":"直した","html_text":"<body>直した</body>"}`,
	},
	KindDeleteStory:      {`{"story":"s1","text":"消すコメント"}`},
	KindDeleteAttachment: {`{"attachment":"a1","name":"図.png"}`, `{"attachment":"local:a1","name":"図.png","path":"/data/attachments/a1"}`},
	KindMoveMySection:    {`{"section":"ms1","section_name":"今日","workspace":"w1"}`},
	KindUpdateProject: {
		`{"notes":"説明","html_notes":"<body>説明</body>"}`,
		`{"owner":"","start_on":"","due_on":""}`,
		`{"owner":"u1","start_on":"2026-11-01","due_on":"2026-12-01","color":"dark-red"}`,
		`{"color":"none"}`,
	},
	KindUpdateProjectBrief: {
		`{"delete":true}`,
		`{"title":"ブリーフ","html_text":"<body>本文</body>","text":"本文"}`,
		`{"title":""}`,
		`{"html_text":"<body></body>","text":""}`,
	},
	KindAddTimeEntry:    {`{"entry":"local:e1","duration_minutes":45,"entered_on":"2026-11-20"}`},
	KindUpdateTimeEntry: {`{"entry":"e1","duration_minutes":0,"entered_on":"2026-11-20"}`},
	KindDeleteTimeEntry: {`{"entry":"e1","duration_minutes":1440,"entered_on":"2026-11-19"}`},
	KindCreateEnumOption: {
		`{"field":"cf1","field_name":"優先度","name":"最高","workspace":"w1"}`,
		`{"field":"cf1","field_name":"優先度","name":"最高","workspace":""}`,
	},
	KindAddFieldSetting:    {`{"field":"cf1","field_name":"優先度","workspace":"w1"}`},
	KindRemoveFieldSetting: {`{"field":"cf1","field_name":"優先度","workspace":""}`},
}

// stored returns a payload as the store reads it back (json.Unmarshal into a map).
func stored(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%s: %v", s, err)
	}
	return m
}

// canonical returns m as JSON with sorted keys.
func canonical(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEveryKindHasAPayload(t *testing.T) {
	for _, k := range AllKinds() {
		typ, ok := PayloadType(k)
		if !ok {
			t.Errorf("%s: no payload struct", k)
			continue
		}
		if typ.Kind() != reflect.Struct {
			t.Errorf("%s: payload %v is not a struct", k, typ)
		}
		if len(storedPayloads[k]) == 0 {
			t.Errorf("%s: no stored payload in storedPayloads", k)
		}
	}
	if len(payloadTypes) != len(AllKinds()) {
		t.Errorf("%d payload structs for %d kinds", len(payloadTypes), len(AllKinds()))
	}
}

// Every payload as stored reads into its kind's struct and is written back the same:
// the stored form does not change.
func TestPayloadRoundTrip(t *testing.T) {
	for k, payloads := range storedPayloads {
		for _, s := range payloads {
			m := stored(t, s)
			p, err := DecodePayload(Op{Kind: k, Payload: m})
			if err != nil {
				t.Errorf("%s %s: %v", k, s, err)
				continue
			}
			back, err := EncodePayload(k, p)
			if err != nil {
				t.Errorf("%s %s: encode: %v", k, s, err)
				continue
			}
			if got, want := canonical(t, back), canonical(t, m); got != want {
				t.Errorf("%s: round trip\n got %s\nwant %s", k, got, want)
			}
		}
	}
}

// Payloads as written before they were read back (ints, []string, nested maps of the Go
// types the store builds them with) read the same as once stored.
func TestPayloadFromGoValues(t *testing.T) {
	op := Op{Kind: KindCreateTask, Payload: map[string]any{"workspace": "w1", "name": "t",
		"custom_fields": map[string]any{"cf3": []string{"u1"}, "cf2": 3}}}
	p, err := PayloadOf[CreateTask](op)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.CustomFields["cf3"]; !reflect.DeepEqual(got, []any{"u1"}) {
		t.Errorf("cf3 = %#v", got)
	}
	if got := p.CustomFields["cf2"]; got != float64(3) {
		t.Errorf("cf2 = %#v", got)
	}
	e, err := PayloadOf[TimeEntry](Op{Kind: KindAddTimeEntry, Payload: map[string]any{"entry": "e", "duration_minutes": 45, "entered_on": "2026-11-20"}})
	if err != nil || e.DurationMinutes != 45 {
		t.Fatalf("time entry = %+v, %v", e, err)
	}
	e, err = PayloadOf[TimeEntry](Op{Kind: KindAddTimeEntry, Payload: map[string]any{"entry": "e", "duration_minutes": float64(45), "entered_on": "2026-11-20"}})
	if err != nil || e.DurationMinutes != 45 {
		t.Fatalf("time entry (read back) = %+v, %v", e, err)
	}
}

// Payloads written by older versions read: keys they did not write yet are zero.
func TestOlderPayloadsRead(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		s    string
	}{
		{KindDeleteTag, `{"name":"タグ","color":"","tasks":["t1"]}`},             // before #68: no workspace
		{KindCreateEnumOption, `{"field":"cf1","field_name":"f","name":"o"}`},  // no workspace yet
		{KindAddFieldSetting, `{"field":"cf1","field_name":"f"}`},              // no workspace yet
		{KindRemoveFieldSetting, `{"field":"cf1","field_name":"f"}`},           // no workspace yet
		{KindAddComment, `{"text":"t"}`},                                       // no story_gid
		{KindDeleteAttachment, `{"attachment":"a1"}`},                          // no name
		{KindDeleteTask, `{}`},                                                 // empty
		{KindDeleteSection, `{"project":"p1"}`},                                // project only
		{KindSetCustomField, `{"field":"cf1","type":"text","value":"v"}`},      // no names
		{KindSetCustomField, `{"field":"cf1","field_name":null,"value":null}`}, // nulls
		{KindAddProject, `{"project":"p1"}`},                                   // no names
		{KindUpdateTag, `{"name":"n","workspace":"w1"}`},                       // workspace (documented)
		{KindMoveTask, `{"project":"p1","section":"s1"}`},                      // no section_name
		{KindUpdateProject, `{"color":null}`},                                  // null: absent
		{KindDeleteTask, `null`},                                               // no payload at all
	} {
		var m map[string]any
		if err := json.Unmarshal([]byte(tc.s), &m); err != nil {
			t.Fatal(err)
		}
		if _, err := DecodePayload(Op{Kind: tc.kind, Payload: m}); err != nil {
			t.Errorf("%s %s: %v", tc.kind, tc.s, err)
		}
	}
	d, err := PayloadOf[DeleteTag](Op{Kind: KindDeleteTag, Payload: stored(t, `{"name":"タグ","color":"","tasks":["t1"]}`)})
	if err != nil || d.Workspace != "" || !reflect.DeepEqual(d.Tasks, []string{"t1"}) {
		t.Fatalf("delete_tag = %+v, %v", d, err)
	}
}

// A key the struct does not know or a value of another type is an error: such an entry
// is never sent with a part of it left out or misread.
func TestUnreadablePayloads(t *testing.T) {
	for _, tc := range []struct {
		kind Kind
		s    string
	}{
		{KindUpdateTask, `{"due_at":"2026-11-20T10:00:00Z"}`}, // a field of a newer version
		{KindUpdateTask, `{"completed":"true"}`},
		{KindUpdateTask, `{"name":3}`},
		{KindCreateTask, `{"workspace":"w1","name":"t","parent":7}`},
		{KindCreateTask, `{"workspace":"w1","name":"t","custom_fields":["x"]}`},
		{KindMarkNotification, `{"read":"yes"}`},
		{KindAddTimeEntry, `{"entry":"e","duration_minutes":1.5,"entered_on":"2026-11-20"}`},
		{KindAddTimeEntry, `{"entry":"e","duration_minutes":"45","entered_on":"2026-11-20"}`},
		{KindDeleteTag, `{"name":"t","tasks":"t1"}`},
		{KindAddTag, `{"tag":1}`},
		{KindAddTag, `{"tag":"t","tag_name":"x","workspace":"w","html_text":"x"}`},
		{KindUpdateProjectBrief, `{"delete":"true"}`},
		{KindCreateSection, `{"project":"p","name":"n","existing":[1]}`},
	} {
		_, err := DecodePayload(Op{Kind: tc.kind, Payload: stored(t, tc.s)})
		var pe *PayloadError
		if !errors.As(err, &pe) || pe.Kind != tc.kind {
			t.Errorf("%s %s: err = %v, want a *PayloadError", tc.kind, tc.s, err)
		}
	}
}

func TestUnknownKindPayload(t *testing.T) {
	op := Op{Kind: "something_new", Payload: map[string]any{"x": 1}}
	if _, err := DecodePayload(op); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("DecodePayload = %v", err)
	}
	if _, err := PayloadOf[DeleteTask](op); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("PayloadOf = %v", err)
	}
	if err := Readable(op); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("Readable = %v", err)
	}
	if _, err := EncodePayload("something_new", DeleteTask{}); !errors.Is(err, ErrUnknownKind) {
		t.Errorf("EncodePayload = %v", err)
	}
}

func TestPayloadOfAnotherKindsStruct(t *testing.T) {
	op := Op{Kind: KindAddFollower, Payload: map[string]any{"user": "u1"}}
	if _, err := PayloadOf[Dependency](op); err == nil || errors.Is(err, ErrUnknownKind) {
		t.Errorf("PayloadOf[Dependency](add_follower) = %v", err)
	}
	if _, err := EncodePayload(KindAddFollower, Dependency{}); err == nil {
		t.Error("EncodePayload(add_follower, Dependency) did not fail")
	}
	if _, err := EncodePayload(KindAddFollower, &Follower{}); err == nil {
		t.Error("EncodePayload(add_follower, *Follower) did not fail")
	}
	defer func() {
		if recover() == nil {
			t.Error("CheckPayloadType[Dependency](add_follower) did not panic")
		}
	}()
	CheckPayloadType[Follower](KindRemoveFollower) // the same struct: fine
	CheckPayloadType[Dependency](KindAddFollower)
}

func TestEncodePayload(t *testing.T) {
	m, err := EncodePayload(KindUpdateTask, UpdateTask{DueOn: new(""), Completed: new(false)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonical(t, m), `{"completed":false,"due_on":""}`; got != want {
		t.Errorf("update_task = %s, want %s", got, want)
	}
	m, err = EncodePayload(KindCreateSection, CreateSection{Project: "p", Name: "n", Existing: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := canonical(t, m), `{"existing":[],"name":"n","project":"p"}`; got != want {
		t.Errorf("create_section = %s, want %s", got, want)
	}
}

// An unreadable payload still reads as far as it can: the fields of the right type.
func TestBestEffortPayload(t *testing.T) {
	op := Op{Kind: KindMoveTask, Payload: stored(t, `{"project":"p1","section":5,"section_name":"s","from_the_future":true}`)}
	p, err := BestEffortPayload[MoveTask](op)
	var pe *PayloadError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
	if p != (MoveTask{Project: "p1", SectionName: "s"}) {
		t.Fatalf("payload = %+v", p)
	}
	p, err = BestEffortPayload[MoveTask](Op{Kind: KindMoveTask, Payload: stored(t, `{"project":"p1","section":"s1","section_name":"s"}`)})
	if err != nil || p != (MoveTask{Project: "p1", Section: "s1", SectionName: "s"}) {
		t.Fatalf("readable = %+v, %v", p, err)
	}
	if p, err := BestEffortPayload[MoveTask](Op{Kind: "something_new", Payload: map[string]any{"project": "p1"}}); !errors.Is(err, ErrUnknownKind) || p != (MoveTask{}) {
		t.Fatalf("unknown kind = %+v, %v", p, err)
	}
	if p, err := BestEffortPayload[Dependency](op); err == nil || p != (Dependency{}) {
		t.Fatalf("another kind's struct = %+v, %v", p, err)
	}
}

// SectionRefs names what the store replaces under SectionRefKeys, for every stored form.
func TestSectionRefs(t *testing.T) {
	for k, payloads := range storedPayloads {
		for _, s := range payloads {
			op := Op{Kind: k, Payload: stored(t, s)}
			var want []string
			for _, key := range SectionRefKeys {
				if g, _ := op.Payload[key].(string); g != "" {
					want = append(want, g)
				}
			}
			if got := SectionRefs(op); !reflect.DeepEqual(got, want) {
				t.Errorf("%s %s: SectionRefs = %v, want %v", k, s, got, want)
			}
		}
	}
}
