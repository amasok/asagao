// Package timetrack holds the rules of time tracking entries (#6): how a duration is
// written and read, and which durations Asana accepts.
package timetrack

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MaxMinutes is the longest duration accepted for one entry (a guard against typos such
// as "90h" for "90m"; Asana itself only asks for more than 0).
const MaxMinutes = 24 * 60

// ErrDuration is returned for a duration that cannot be read.
var ErrDuration = errors.New("時間は 90 / 90m / 1h30m / 1.5h / 1:30 のように書いてください")

// ParseDuration reads a duration in minutes. Accepted forms: "90" (minutes), "90m",
// "2h", "1h30m", "1.5h", "1:30" (hours:minutes), with "分" / "時間" for "m" / "h".
// It must be at least 1 minute and at most MaxMinutes.
func ParseDuration(s string) (int, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer("時間", "h", "分", "m", " ", "").Replace(s)
	if s == "" {
		return 0, ErrDuration
	}
	minutes, err := parse(s)
	if err != nil {
		return 0, err
	}
	if minutes < 1 {
		return 0, errors.New("時間は 1 分以上にしてください")
	}
	if minutes > MaxMinutes {
		return 0, fmt.Errorf("1 件の時間は %s までです", Format(MaxMinutes))
	}
	return minutes, nil
}

func parse(s string) (int, error) {
	if h, m, ok := strings.Cut(s, ":"); ok {
		hours, err1 := digits(h)
		mins, err2 := digits(m)
		if err1 != nil || err2 != nil || len(m) != 2 || mins >= 60 {
			return 0, ErrDuration
		}
		return hours*60 + mins, nil
	}
	if n, err := digits(s); err == nil {
		return n, nil
	}
	total, rest := 0, s
	if h, r, ok := strings.Cut(rest, "h"); ok {
		hours, err := hoursToMinutes(h)
		if err != nil {
			return 0, err
		}
		total, rest = hours, r
		if rest == "" {
			return total, nil
		}
	}
	m, ok := strings.CutSuffix(rest, "m")
	if !ok {
		return 0, ErrDuration
	}
	mins, err := digits(m)
	if err != nil {
		return 0, ErrDuration
	}
	return total + mins, nil
}

// hoursToMinutes reads hours written as "2" or "1.5" (decimal, without float rounding)
// as minutes; a fraction must come out as whole minutes.
func hoursToMinutes(h string) (int, error) {
	whole, frac, dot := strings.Cut(h, ".")
	hours, err := digits(whole)
	if err != nil || (dot && frac == "") || len(frac) > 6 || strings.Trim(frac, "0123456789") != "" {
		return 0, ErrDuration
	}
	minutes := hours * 60
	if frac != "" {
		n, _ := strconv.Atoi(frac)
		scale := 1
		for range frac {
			scale *= 10
		}
		if n*60%scale != 0 {
			return 0, errors.New("時間は分単位にしてください (1.5h は 90 分)")
		}
		minutes += n * 60 / scale
	}
	return minutes, nil
}

// digits reads a non-negative whole number of at most 6 digits.
func digits(s string) (int, error) {
	if s == "" || len(s) > 6 || strings.Trim(s, "0123456789") != "" {
		return 0, ErrDuration
	}
	return strconv.Atoi(s)
}

// Format writes minutes as "1h30m", "2h" or "45m" (ParseDuration reads it back).
func Format(minutes int) string {
	sign, u := "", uint64(minutes)
	if minutes < 0 {
		// Negated as unsigned: -math.MinInt overflows back to itself as an int, and
		// recursing on it never ended (a NaN duration became math.MinInt on amd64).
		sign, u = "-", -u
	}
	h, m := u/60, u%60
	switch {
	case h == 0:
		return fmt.Sprintf("%s%dm", sign, m)
	case m == 0:
		return fmt.Sprintf("%s%dh", sign, h)
	}
	return fmt.Sprintf("%s%dh%02dm", sign, h, m)
}
