package project

import "testing"

func TestValidateFields(t *testing.T) {
	for _, ok := range []map[string]any{
		{"notes": "x", "html_notes": "<body>x</body>"},
		{"owner": ""}, {"owner": "123"},
		{"start_on": "2026-12-01"}, {"due_on": ""},
		{"color": "dark-warm-gray"}, {"color": "none"}, {"color": "blue"}, {"color": " Red "},
	} {
		if err := ValidateFields(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, bad := range []map[string]any{
		{}, {"name": "x"}, {"archived": true}, {"owner": nil}, {"due_on": 20261201},
		{"color": "chartreuse"}, {"color": ""}, {"color": "light-brown"}, {"start_on": "2026/12/01"}, {"due_on": "明日"},
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

// The names Asana's GET answers are sent as a written name; the input is not changed (#52).
func TestNormalizeFieldsColor(t *testing.T) {
	for in, want := range map[string]string{"red": "dark-red", " Blue ": "dark-blue", "cool-gray": "dark-warm-gray",
		"yellow": "dark-brown", "light-yellow": "light-yellow", "NONE": "none", "dark-teal": "dark-teal"} {
		changes := map[string]any{"color": in, "notes": "x"}
		got, err := NormalizeFields(changes)
		if err != nil || got["color"] != want || got["notes"] != "x" {
			t.Errorf("NormalizeFields(%q) = %v, %v; want %q", in, got, err, want)
		}
		if changes["color"] != in {
			t.Errorf("the input changed: %v", changes)
		}
	}
	if _, err := NormalizeFields(map[string]any{"color": "chartreuse"}); err == nil {
		t.Error("chartreuse accepted")
	}
}
