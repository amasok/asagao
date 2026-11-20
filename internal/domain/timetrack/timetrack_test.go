package timetrack

import (
	"math"
	"strconv"
	"testing"
)

func TestParseDuration(t *testing.T) {
	for in, want := range map[string]int{
		"90": 90, "90m": 90, "2h": 120, "1h30m": 90, "1h05m": 65, "1.5h": 90, "0.25h": 15, "1.1h": 66, "0.7h": 42,
		"1:30": 90, "0:05": 5, " 45 m ": 45, "1時間30分": 90, "30分": 30, "2H": 120, "24h": 1440,
	} {
		got, err := ParseDuration(in)
		if err != nil || got != want {
			t.Errorf("ParseDuration(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0", "0m", "0h", "-5", "-1h", "abc", "1h30", "1:3", "1:60", "1.01h", "1.h", ".5h", "h", "m", "1h1h",
		"1441", "25h", "1e2", "1:30m", "1.5", "99999999", "1_0", "+5", "１０"} {
		if got, err := ParseDuration(in); err == nil {
			t.Errorf("ParseDuration(%q) = %d; want an error", in, got)
		}
	}
}

func TestFormatRoundTrips(t *testing.T) {
	for m, want := range map[int]string{0: "0m", 5: "5m", 60: "1h", 65: "1h05m", 150: "2h30m", 1440: "24h", -90: "-1h30m"} {
		if got := Format(m); got != want {
			t.Errorf("Format(%d) = %q; want %q", m, got, want)
		}
		if m > 0 {
			if back, err := ParseDuration(Format(m)); err != nil || back != m {
				t.Errorf("ParseDuration(Format(%d)) = %d, %v", m, back, err)
			}
		}
	}
}

// The most negative int is its own negation: Format must not recurse on it forever (a NaN
// duration converted to int is math.MinInt on amd64; CI caught the stack overflow).
// math.MinInt depends on the size of int, so the test also builds for 32-bit targets.
func TestFormatNegativeExtremes(t *testing.T) {
	minWant := "-153722867280912930h08m"
	if strconv.IntSize == 32 {
		minWant = "-35791394h08m"
	}
	for in, want := range map[int]string{
		-1:          "-1m",
		-90:         "-1h30m",
		-120:        "-2h",
		math.MinInt: minWant,
	} {
		if got := Format(in); got != want {
			t.Errorf("Format(%d) = %q, want %q", in, got, want)
		}
	}
}
