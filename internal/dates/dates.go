// Package dates parses the relative and absolute dates accepted by the CLI, TUI and MCP tools.
package dates

import (
	"fmt"
	"time"
)

// Parse converts s to YYYY-MM-DD relative to now. Accepted forms:
//
//	YYYY-MM-DD, today/今日, tomorrow/明日, 明後日, yesterday/昨日, +Nd, -Nd
//
// An empty string returns "".
func Parse(s string, now time.Time) (string, error) {
	day := func(n int) string { return now.AddDate(0, 0, n).Format(time.DateOnly) }
	switch s {
	case "":
		return "", nil
	case "today", "今日":
		return day(0), nil
	case "tomorrow", "明日":
		return day(1), nil
	case "明後日":
		return day(2), nil
	case "yesterday", "昨日":
		return day(-1), nil
	}
	if len(s) >= 3 && (s[0] == '+' || s[0] == '-') && s[len(s)-1] == 'd' {
		var n int
		if _, err := fmt.Sscanf(s[1:len(s)-1], "%d", &n); err == nil {
			if s[0] == '-' {
				n = -n
			}
			return day(n), nil
		}
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return "", fmt.Errorf("日付の形式が不正です: %q（YYYY-MM-DD / today / tomorrow / 明日 / +Nd）", s)
	}
	return t.Format(time.DateOnly), nil
}
