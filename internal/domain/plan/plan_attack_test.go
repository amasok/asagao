package plan

import (
	"strings"
	"testing"
	"time"
)

func TestOffForBoundaries(t *testing.T) {
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	v := at.Format(time.RFC3339)
	jst := time.FixedZone("JST", 9*3600)
	cases := []struct {
		name  string
		value string
		now   time.Time
		want  bool
	}{
		{"same instant", v, at, true},
		{"1ns before the day", v, at.Add(RecheckAfter - time.Nanosecond), true},
		{"exactly the day", v, at.Add(RecheckAfter), false},
		{"after the day", v, at.Add(RecheckAfter + time.Second), false},
		{"clock behind by 1s", v, at.Add(-time.Second), false},
		{"clock far behind", v, at.Add(-30 * 24 * time.Hour), false},
		{"empty", "", at, false},
		{"garbage", "yes", at, false},
		{"date only", "2026-11-17", at, false},
		{"other zone, same instant", at.In(jst).Format(time.RFC3339), at.Add(time.Hour), true},
		{"now in another zone", v, at.Add(time.Hour).In(jst), true},
	}
	for _, c := range cases {
		if got := OffFor(c.value, c.now); got != c.want {
			t.Errorf("%s: OffFor = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOffAtZeroAndFuture(t *testing.T) {
	zero := time.Time{}
	if OffAt(zero, zero) {
		t.Fatal("OffAt(zero, zero) = true, want false")
	}
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	if OffAt(zero, at) {
		t.Fatal("OffAt(zero, at) = true, want false")
	}
	if OffAt(at, at.Add(-time.Nanosecond)) {
		t.Fatal("OffAt(at, at-1ns) = true, want false")
	}
	if !OffAt(at, at.Add(RecheckAfter-time.Nanosecond)) {
		t.Fatal("OffAt(at, at+24h-1ns) = false, want true")
	}
	if OffAt(at, at.Add(RecheckAfter)) {
		t.Fatal("OffAt(at, at+24h) = true, want false")
	}
}

func TestFeatureConstantsDistinctAndStable(t *testing.T) {
	wants := map[string]string{
		"Sections":         "sections",
		"Tags":             "tags",
		"MySection":        "my_section",
		"Search":           "search",
		"Typeahead":        "typeahead",
		"CustomID":         "custom_id",
		"TimeTracking":     "time_tracking",
		"Goals":            "goals",
		"Portfolios":       "portfolios",
		"CustomFields":     "custom_fields",
		"ProjectBrief":     "project_brief",
		"StartOn":          "start_on",
		"CustomTypes":      "custom_types",
		"TaskTemplates":    "task_templates",
		"ProjectTemplates": "project_templates",
	}
	got := map[string]string{
		"Sections":         Sections,
		"Tags":             Tags,
		"MySection":        MySection,
		"Search":           Search,
		"Typeahead":        Typeahead,
		"CustomID":         CustomID,
		"TimeTracking":     TimeTracking,
		"Goals":            Goals,
		"Portfolios":       Portfolios,
		"CustomFields":     CustomFields,
		"ProjectBrief":     ProjectBrief,
		"StartOn":          StartOn,
		"CustomTypes":      CustomTypes,
		"TaskTemplates":    TaskTemplates,
		"ProjectTemplates": ProjectTemplates,
	}
	seen := make(map[string]string, len(wants))
	for name, val := range got {
		if val != wants[name] {
			t.Errorf("%s = %q, want %q", name, val, wants[name])
		}
		if prev, dup := seen[val]; dup {
			t.Errorf("duplicate value %q for %s and %s", val, prev, name)
		}
		seen[val] = name
		if strings.Contains(val, ":") {
			t.Errorf("%s value %q must not contain separator ':'", name, val)
		}
	}
}
