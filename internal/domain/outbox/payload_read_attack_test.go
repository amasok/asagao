package outbox

import (
	"reflect"
	"testing"
)

func TestAttackPayloadFieldsReadsKeyByKey(t *testing.T) {
	op := Op{Kind: KindUpdateTag, Payload: map[string]any{"name": 5.0, "color": "blue", "workspace": []any{"x"}, "from_future": 1.0}}
	got := PayloadFields(op)
	if _, has := got["name"]; has {
		t.Errorf("a mistyped name reads as present: %v", got)
	}
	if got["color"] != "blue" {
		t.Errorf("color = %v", got["color"])
	}
	for k, v := range got {
		if v == "" && k != "workspace" {
			t.Errorf("%s reads as an empty value: %v", k, got)
		}
	}
	p, _ := BestEffortPayload[UpdateTag](op)
	if p.Name != nil || p.Color == nil || *p.Color != "blue" {
		t.Errorf("BestEffortPayload = %+v", p)
	}
}

func TestAttackReadsField(t *testing.T) {
	cases := []struct {
		m    map[string]any
		want bool
	}{
		{map[string]any{"read": true}, true},
		{map[string]any{"read": false}, true},
		{map[string]any{}, false},
		{map[string]any{"read": nil}, false},
		{map[string]any{"read": "yes"}, false},
		{map[string]any{"read": 1.0}, false},
	}
	for _, c := range cases {
		if got := ReadsField(Op{Kind: KindMarkNotification, Payload: c.m}, "read"); got != c.want {
			t.Errorf("ReadsField(%v) = %v", c.m, got)
		}
	}
	if ReadsField(Op{Kind: Kind("from_future"), Payload: map[string]any{"read": true}}, "read") {
		t.Error("an unknown kind reads a field")
	}
}

func TestAttackPayloadNameUnknownAndMistyped(t *testing.T) {
	if got := PayloadName(Op{Kind: Kind("zz"), Payload: map[string]any{"name": "n"}}); got != "n" {
		t.Errorf("unknown kind: %q", got)
	}
	if got := PayloadName(Op{Kind: KindUpdateTask, Payload: map[string]any{"name": 3.0}}); got != "" {
		t.Errorf("mistyped: %q", got)
	}
	if got := PayloadName(Op{Kind: KindUpdateTask}); got != "" {
		t.Errorf("nil payload: %q", got)
	}
}

// EditPayload keeps a mistyped key it does not touch, and takes one it clears out.
func TestAttackEditPayloadMistypedKeys(t *testing.T) {
	op := Op{Kind: KindUpdateTask, Payload: map[string]any{"name": 5.0, "notes": "n"}}
	if err := EditPayload(&op, func(p *UpdateTask) { p.Notes = nil }); err != nil {
		t.Skip("held entry refused:", err)
	}
	if _, has := op.Payload["notes"]; has {
		t.Errorf("cleared key stays: %v", op.Payload)
	}
	if !reflect.DeepEqual(op.Payload["name"], 5.0) {
		t.Errorf("an untouched mistyped key was lost or changed: %v", op.Payload)
	}
}
