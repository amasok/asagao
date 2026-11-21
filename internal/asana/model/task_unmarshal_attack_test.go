package model

import (
	"encoding/json"
	"testing"
)

func TestTaskUnmarshalStartOnEdges(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		known    bool
		start    string
		wantErr  bool
	}{
		{"date", `{"gid":"x","start_on":"2026-12-01"}`, true, "2026-12-01", false},
		{"null", `{"gid":"x","start_on":null}`, true, "", false},
		{"absent", `{"gid":"x"}`, false, "", false},
		{"empty string", `{"gid":"x","start_on":""}`, true, "", false},
		{"number", `{"gid":"x","start_on":1}`, false, "", false},
		{"object", `{"gid":"x","start_on":{"a":1}}`, false, "", false},
		{"array", `{"gid":"x","start_on":[]}`, false, "", false},
		{"bool", `{"gid":"x","start_on":true}`, false, "", false},
		{"upper-case key", `{"gid":"x","START_ON":"2026-12-02"}`, true, "2026-12-02", false},
		{"duplicate key, last wins", `{"gid":"x","start_on":"2026-12-01","start_on":null}`, true, "", false},
		{"bad type elsewhere still errors", `{"gid":1,"start_on":"2026-12-01"}`, true, "2026-12-01", true},
		{"malformed", `{"gid":"x","start_on":`, false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got Task
			err := json.Unmarshal([]byte(tc.in), &got)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if got.StartOnKnown != tc.known || got.StartOn != tc.start {
				t.Errorf("known=%v start=%q, want %v %q", got.StartOnKnown, got.StartOn, tc.known, tc.start)
			}
		})
	}
}

// A task decoded into a value that already holds one does not keep the old start date.
func TestTaskUnmarshalReusedValue(t *testing.T) {
	task := Task{StartOn: "2026-01-01", StartOnKnown: true}
	if err := json.Unmarshal([]byte(`{"gid":"x"}`), &task); err != nil {
		t.Fatal(err)
	}
	if task.StartOnKnown || task.StartOn != "" {
		t.Errorf("stale start date kept: %+v", task)
	}
}

// Every other field still reads, in a slice, a struct field and an envelope, and the
// fields that have a decoder of their own (assignee_section) keep it.
func TestTaskUnmarshalKeepsOtherFields(t *testing.T) {
	var env struct {
		Data  []Task `json:"data"`
		One   Task   `json:"one"`
		Ptr   *Task  `json:"ptr"`
		ByKey map[string]Task
	}
	in := `{"data":[{"gid":"a","name":"n","due_on":"2026-12-10","start_on":"2026-12-01","completed":true,
		"assignee_section":{"gid":"s","name":"S"},"custom_fields":[{"gid":"c"}],"tags":[{"gid":"g"}]},null,{"gid":"b"}],
		"one":{"gid":"o","start_on":null},"ptr":{"gid":"p"},"ByKey":{"k":{"gid":"k","start_on":"2026-12-03"}}}`
	if err := json.Unmarshal([]byte(in), &env); err != nil {
		t.Fatal(err)
	}
	a := env.Data[0]
	if a.GID != "a" || a.Name != "n" || a.DueOn != "2026-12-10" || !a.Completed || len(a.CustomFields) != 1 || len(a.Tags) != 1 ||
		!a.AssigneeSection.Set || a.AssigneeSection.Ref == nil || a.AssigneeSection.Ref.GID != "s" {
		t.Errorf("fields lost: %+v", a)
	}
	if env.Data[1].StartOnKnown || env.Data[1].GID != "" {
		t.Errorf("null element: %+v", env.Data[1])
	}
	if !env.One.StartOnKnown || env.Ptr == nil || env.Ptr.StartOnKnown || !env.ByKey["k"].StartOnKnown {
		t.Errorf("one=%+v ptr=%+v key=%+v", env.One, env.Ptr, env.ByKey["k"])
	}
}

// Marshaling a task still gives start_on (the shadow field is only for reading).
func TestTaskMarshalStillHasStartOn(t *testing.T) {
	b, err := json.Marshal(Task{GID: "x", StartOn: "2026-12-01", StartOnKnown: true})
	if err != nil {
		t.Fatal(err)
	}
	var back Task
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.StartOn != "2026-12-01" || !back.StartOnKnown {
		t.Errorf("round trip: %s -> %+v", b, back)
	}
}
