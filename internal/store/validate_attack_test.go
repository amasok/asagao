package store

import (
	"context"
	"testing"
)

// A rejected edit writes nothing, whichever rule rejects it.
func TestUpdateTaskLocalRejectsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	for name, ch := range map[string]map[string]any{
		"blank name":                     {"name": " \t　"},
		"empty":                          {},
		"unknown field":                  {"bogus": "x"},
		"completed string":               {"completed": "true"},
		"name bool":                      {"name": true},
		"bad subtype":                    {"resource_subtype": "nope"},
		"bad approval":                   {"approval_status": "nope"},
		"approval, not an approval task": {"approval_status": "approved"},
	} {
		s := newTestStore(t)
		if err := s.UpdateTaskLocal(ctx, "t1", ch, testNow); err == nil {
			t.Errorf("%s: no error", name)
		}
		if ops, _ := s.ListOps(ctx); len(ops) != 0 {
			t.Errorf("%s: wrote %d outbox entries", name, len(ops))
		}
	}
}
