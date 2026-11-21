package outbox

import "testing"

// A value of another type leaves its field zero: a pointer nil, not pointing to "" (which
// would clear what it names when it is applied).
func TestBestEffortPayloadLeavesMistypedPointersNil(t *testing.T) {
	op := Op{Kind: KindUpdateTask, Payload: map[string]any{"name": 5.0, "due_on": "2027-01-01", "from_future": 1.0}}
	p, err := BestEffortPayload[UpdateTask](op)
	if err == nil {
		t.Error("err = nil, want a *PayloadError")
	}
	if p.Name != nil || Str(p.DueOn) != "2027-01-01" {
		t.Errorf("payload = name %v, due_on %v", p.Name, p.DueOn)
	}
	if f := PayloadFields(op); len(f) != 1 || f["due_on"] != "2027-01-01" {
		t.Errorf("PayloadFields = %v", f)
	}
	op = Op{Kind: KindCreateSection, Payload: map[string]any{"project": "p1", "name": "S", "existing": []any{"s1", 2.0}}}
	if s, _ := BestEffortPayload[CreateSection](op); s.Project != "p1" || s.Name != "S" || s.Existing != nil {
		t.Errorf("CreateSection = %+v", s)
	}
}

func TestReadsField(t *testing.T) {
	for _, c := range []struct {
		payload map[string]any
		want    bool
	}{
		{map[string]any{"read": true}, true},
		{map[string]any{"read": false, "from_future": 1.0}, true},
		{map[string]any{"workspace": "w"}, false},
		{map[string]any{"read": nil}, false},
		{map[string]any{"read": "yes"}, false},
		{map[string]any{"read": 1.0}, false},
	} {
		if got := ReadsField(Op{Kind: KindMarkNotification, Payload: c.payload}, "read"); got != c.want {
			t.Errorf("ReadsField(%v) = %v, want %v", c.payload, got, c.want)
		}
	}
	if ReadsField(Op{Kind: "from_future", Payload: map[string]any{"read": true}}, "read") {
		t.Error("ReadsField of an unknown kind = true")
	}
}
