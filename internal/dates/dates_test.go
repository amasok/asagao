package dates

import (
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	now := time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local)
	for in, want := range map[string]string{
		"": "", "today": "2026-11-13", "今日": "2026-11-13", "tomorrow": "2026-11-14", "明日": "2026-11-14",
		"明後日": "2026-11-15", "yesterday": "2026-11-12", "+7d": "2026-11-20", "-3d": "2026-11-10",
		"+30d": "2026-12-13", "2026-12-01": "2026-12-01",
	} {
		if got, err := Parse(in, now); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"7d", "+d", "2026/12/01", "next week", "来週"} {
		if _, err := Parse(bad, now); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}
