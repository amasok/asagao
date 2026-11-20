package plan

import (
	"testing"
	"time"

	"github.com/amasok/asagao/internal/domain/outbox"
)

func TestOffFor(t *testing.T) {
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	for v, want := range map[string]bool{
		"": false, "x": false,
		now.Format(time.RFC3339):                                  true,
		now.Add(-RecheckAfter + time.Second).Format(time.RFC3339): true,
		now.Add(-RecheckAfter).Format(time.RFC3339):               false,
		now.Add(time.Minute).Format(time.RFC3339):                 false,
	} {
		if got := OffFor(v, now); got != want {
			t.Errorf("%q: %v, want %v", v, got, want)
		}
	}
}

func TestWindow(t *testing.T) {
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	from, until, off := Window(at, at.Add(time.Hour))
	if !off || !from.Equal(at) || !until.Equal(at.Add(RecheckAfter)) {
		t.Fatalf("Window(active) = (%v, %v, %v), want (%v, %v, true)", from, until, off, at, at.Add(RecheckAfter))
	}
	from, until, off = Window(at, at.Add(RecheckAfter))
	if off || !from.IsZero() || !until.IsZero() {
		t.Fatalf("Window(expired) = (%v, %v, %v), want zero/false", from, until, off)
	}
}

func TestFeatureOf(t *testing.T) {
	cases := map[string]string{
		outbox.KindCreateSection:      Sections,
		outbox.KindDeleteSection:      Sections,
		outbox.KindInsertSection:      Sections,
		outbox.KindUpdateTag:          Tags,
		outbox.KindDeleteTag:          Tags,
		outbox.KindMoveMySection:      MySection,
		outbox.KindAddTimeEntry:       TimeTracking,
		outbox.KindUpdateTimeEntry:    TimeTracking,
		outbox.KindDeleteTimeEntry:    TimeTracking,
		outbox.KindCreateEnumOption:   CustomFields,
		outbox.KindAddFieldSetting:    CustomFields,
		outbox.KindRemoveFieldSetting: CustomFields,
		outbox.KindUpdateProjectBrief: ProjectBrief,
		outbox.KindCreateTask:         "",
		"unknown":                     "",
	}
	for kind, want := range cases {
		if got := FeatureOf(kind); got != want {
			t.Errorf("FeatureOf(%q) = %q, want %q", kind, got, want)
		}
	}
}
