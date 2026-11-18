package project

import "testing"

func TestValidateFields(t *testing.T) {
	for _, ok := range []map[string]any{
		{"notes": "x", "html_notes": "<body>x</body>"},
		{"owner": ""}, {"owner": "123"},
		{"start_on": "2026-12-01"}, {"due_on": ""},
		{"color": "dark-warm-gray"}, {"color": "none"},
	} {
		if err := ValidateFields(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range []map[string]any{
		{}, {"name": "x"}, {"archived": true}, {"owner": nil}, {"due_on": 20261201},
		{"color": "blue"}, {"color": ""}, {"start_on": "2026/12/01"}, {"due_on": "明日"},
	} {
		if err := ValidateFields(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestCheckDates(t *testing.T) {
	for _, c := range []struct {
		start, due string
		ok         bool
	}{
		{"", "", true}, {"", "2026-12-31", true}, {"2026-12-01", "2026-12-31", true}, {"2026-12-31", "2026-12-31", true},
		{"2026-12-01", "", false}, {"2027-01-01", "2026-12-31", false},
	} {
		if err := CheckDates(c.start, c.due); (err == nil) != c.ok {
			t.Errorf("CheckDates(%q, %q) = %v", c.start, c.due, err)
		}
	}
}

func TestFieldLabel(t *testing.T) {
	for _, f := range Fields {
		if FieldLabel(f) == "" {
			t.Errorf("%s has no label", f)
		}
	}
	if FieldLabel("name") != "" {
		t.Error("label for an unknown field")
	}
}
