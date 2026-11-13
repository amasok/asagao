package cli

import (
	"bytes"
	"testing"
	"time"
)

func TestParseDate(t *testing.T) {
	now := time.Date(2026, 11, 13, 15, 0, 0, 0, time.Local)
	for in, want := range map[string]string{
		"": "", "today": "2026-11-13", "tomorrow": "2026-11-14", "yesterday": "2026-11-12",
		"+7d": "2026-11-20", "-3d": "2026-11-10", "2026-12-01": "2026-12-01",
	} {
		if got, err := parseDate(in, now); err != nil || got != want {
			t.Errorf("parseDate(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"7d", "2026/12/01", "next week"} {
		if _, err := parseDate(bad, now); err == nil {
			t.Errorf("parseDate(%q) should fail", bad)
		}
	}
}

func TestPrintTableAlignsWideCharacters(t *testing.T) {
	var b bytes.Buffer
	printTable(&b, [][]string{{"ソファ", "x"}, {"ab", "y"}})
	if got, want := b.String(), "ソファ  x\nab      y\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
