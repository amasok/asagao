package readmodel

import (
	"encoding/json"
	"testing"
)

// How each type is cleared, as the real Asana takes it (#31): people with [], the others
// with null (a date as null too, not {"date": null}).
func TestCustomFieldAPIValueClear(t *testing.T) {
	for typ, want := range map[string]string{
		CFPeople: `[]`, CFDate: `null`, CFNumber: `null`, CFText: `null`, CFEnum: `null`,
	} {
		for _, empty := range []any{nil, "", []any{}, []string{}} {
			b, _ := json.Marshal(CustomFieldAPIValue(typ, empty))
			if NormalizeCustomFieldValue(empty) == nil && string(b) != want {
				t.Errorf("%s cleared with %#v: sent %s, want %s", typ, empty, b, want)
			}
		}
	}
	b, _ := json.Marshal(CustomFieldAPIValue(CFPeople, []any{"u1"}))
	if string(b) != `["u1"]` {
		t.Errorf("people value sent as %s", b)
	}
}
