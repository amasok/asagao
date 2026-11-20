package plan

import (
	"strings"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/outbox"
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

func TestPlanWindowBoundaries(t *testing.T) {
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	jst := time.FixedZone("JST", 9*3600)
	zero := time.Time{}

	cases := []struct {
		name      string
		refusedAt time.Time
		now       time.Time
		wantOff   bool
	}{
		{"zero refusedAt with zero now", zero, zero, false},
		{"zero refusedAt with valid now", zero, at, false},
		{"refusedAt 1ns in future", at, at.Add(-time.Nanosecond), false},
		{"refusedAt far in future", at.Add(48 * time.Hour), at, false},
		{"exactly 0 elapsed", at, at, true},
		{"24h - 1ns elapsed", at, at.Add(RecheckAfter - time.Nanosecond), true},
		{"exactly 24h elapsed", at, at.Add(RecheckAfter), false},
		{"24h + 1ns elapsed", at, at.Add(RecheckAfter + time.Nanosecond), false},
		{"different timezones, active", at.In(jst), at.Add(RecheckAfter - time.Nanosecond), true},
		{"different timezones, expired", at, at.Add(RecheckAfter).In(jst), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			from, until, off := Window(tc.refusedAt, tc.now)
			if off != tc.wantOff {
				t.Fatalf("Window off = %v, want %v", off, tc.wantOff)
			}
			if !off {
				if !from.IsZero() || !until.IsZero() {
					t.Fatalf("Window inactive returned non-zero (%v, %v)", from, until)
				}
				return
			}
			if !from.Equal(tc.refusedAt) || !until.Equal(tc.refusedAt.Add(RecheckAfter)) {
				t.Fatalf("Window = (%v, %v), want (%v, %v)", from, until, tc.refusedAt, tc.refusedAt.Add(RecheckAfter))
			}
		})
	}
}

func TestPlanOffAtAndOffForExtended(t *testing.T) {
	at := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	jst := time.FixedZone("JST", 9*3600)

	if OffAt(at, at.Add(RecheckAfter+time.Nanosecond)) {
		t.Fatal("OffAt(24h+1ns) = true, want false")
	}
	if OffAt(at.Add(time.Nanosecond), at) {
		t.Fatal("OffAt(future) = true, want false")
	}

	// RFC3339Nano, timezone offsets, and malformed strings.
	nano := at.Add(123456789 * time.Nanosecond).In(jst).Format(time.RFC3339Nano)
	if !OffFor(nano, at.Add(RecheckAfter)) {
		t.Fatal("OffFor(RFC3339Nano) should still be active at at+24h because sub-second puts it < 24h")
	}
	if OffFor(nano, at.Add(RecheckAfter+123456789*time.Nanosecond)) {
		t.Fatal("OffFor(RFC3339Nano) should expire exactly 24h after nano timestamp")
	}
	for _, bad := range []string{"", "2026-11-17T10:00:00", "not-a-timestamp", "1700000000", "\x00", "2026-13-45T99:99:99Z"} {
		if OffFor(bad, at) {
			t.Errorf("OffFor(%q) = true, want false", bad)
		}
	}
	if OffFor(at.Add(time.Nanosecond).Format(time.RFC3339Nano), at) {
		t.Fatal("OffFor(future) = true, want false")
	}
}

func TestPlanFeatureOfNonGated(t *testing.T) {
	// FeatureOf edge cases: all plan-gated kinds and non-gated/empty/unknown kinds.
	nonGated := []outbox.Kind{
		"", "unknown", outbox.KindCreateTask, outbox.KindUpdateTask, outbox.KindDeleteTask,
		outbox.KindAddComment, outbox.KindMoveTask, outbox.KindRenameSection,
		outbox.KindAddTag, outbox.KindRemoveTag, outbox.KindSetCustomField,
		outbox.KindUpdateProject, outbox.KindCreateStatus, outbox.KindMarkNotification,
	}
	for _, k := range nonGated {
		if got := FeatureOf(k); got != "" {
			t.Errorf("FeatureOf(%q) = %q, want empty", k, got)
		}
	}

}
