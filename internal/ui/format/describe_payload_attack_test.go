package format

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/amasok/asagao/internal/domain/outbox"
)

// Describing an entry never panics, whatever its payload holds (nothing, nil, values of
// every wrong type, NaN, huge numbers) and for a kind of a newer version.
func TestAttackDescribeUnreadablePayloads(t *testing.T) {
	var wrong map[string]any
	if err := json.Unmarshal([]byte(`{"name":5,"notes":[1],"project":{"a":1},"section":7,"tasks":"t1","tag":true,"user":null,"position":"2","read":"yes","existing":[1,2],"custom_fields":[],"value":{"a":{"b":[null]}},"duration_minutes":1e300,"completed":"x","due_on":1,"delete":"y","title":false,"html_text":3,"section_name":[],"source":{}}`), &wrong); err != nil {
		t.Fatal(err)
	}
	payloads := []map[string]any{nil, {}, wrong, {"name": math.NaN(), "duration_minutes": math.Inf(1), "value": math.NaN()}}
	for _, k := range append(outbox.AllKinds(), "from_the_future") {
		for i, p := range payloads {
			op := outbox.Op{Kind: k, Payload: p, TaskGID: "t1", TaskName: "T"}
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s payload %d: panic %v", k, i, r)
					}
				}()
				_ = DescribeChange(op)
				_ = DescribeOp(op)
				_ = OpLabel(op)
				_, _ = OpNote(op)
			}()
		}
	}
}

// A key of a newer version in the payload does not change what the entry is described as
// (the fields that read still do): the same text as without it.
func TestAttackDescribeIgnoresFutureKeys(t *testing.T) {
	cases := map[outbox.Kind]map[string]any{
		outbox.KindUpdateTask:   {"name": "新", "due_on": "", "completed": true},
		outbox.KindCreateTask:   {"workspace": "w", "name": "n", "notes": "x", "project": "p"},
		outbox.KindMoveTask:     {"project": "p", "section": "s", "section_name": "作業中"},
		outbox.KindAddTag:       {"tag": "g", "tag_name": "重要", "workspace": "w"},
		outbox.KindAddComment:   {"text": "こんにちは"},
		outbox.KindSetParent:    {"parent": "p", "parent_name": "親"},
		outbox.KindUpdateTag:    {"name": "x", "color": ""},
		outbox.KindDeleteTag:    {"name": "t", "color": "c", "workspace": "w", "tasks": []any{"a", "b"}},
		outbox.KindAddTimeEntry: {"entry": "e", "duration_minutes": float64(45), "entered_on": "2026-11-20"},
	}
	for k, p := range cases {
		clean := outbox.Op{Kind: k, Payload: p, TaskName: "T"}
		future := map[string]any{"from_the_future": []any{1}}
		for key, v := range p {
			future[key] = v
		}
		if got, want := DescribeChange(outbox.Op{Kind: k, Payload: future, TaskName: "T"}), DescribeChange(clean); got != want {
			t.Errorf("%s: with a future key %q, without %q", k, got, want)
		}
		if DescribeChange(clean) == "" || DescribeChange(clean) == string(k) {
			t.Errorf("%s: described as %q", k, DescribeChange(clean))
		}
	}
}
