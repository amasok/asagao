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
		"+0d": "2026-11-13", "-0d": "2026-11-13", "+07d": "2026-11-20", "-31d": "2026-10-13",
		"+48d": "2026-12-31", "+49d": "2027-01-01", "+365d": "2027-11-13",
	} {
		if got, err := Parse(in, now); err != nil || got != want {
			t.Errorf("Parse(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"7d", "+d", "-d", "+7", "+7D", "2026/12/01", "next week", "来週",
		"+7xd", "+x7d", "+-3d", "-+3d", "++3d", "+ 7d", "+7 d", "+1.5d", "+99999999999999999999d",
		"2026-13-01", "2026-02-30", " today", "Today",
	} {
		if _, err := Parse(bad, now); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}
