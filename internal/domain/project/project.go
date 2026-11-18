// Package project holds the rules for changing a project's fields offline (the
// update_project outbox entry): which fields, which values.
package project

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Fields are the project fields a local edit may change (the keys of an update_project
// payload). Every value is a string; "" clears owner, start_on and due_on.
var Fields = []string{"notes", "html_notes", "owner", "start_on", "due_on", "color"}

// Colors are the colors Asana accepts for a project ("none" for no color).
var Colors = []string{
	"dark-pink", "dark-green", "dark-blue", "dark-red", "dark-teal", "dark-brown", "dark-orange", "dark-purple", "dark-warm-gray",
	"light-pink", "light-green", "light-blue", "light-red", "light-teal", "light-brown", "light-orange", "light-purple", "light-warm-gray",
	"none",
}

// ValidateFields checks the field changes of a local edit: at least one, each a field that
// can be changed, with a string value, a color Asana has, and dates as YYYY-MM-DD.
func ValidateFields(changes map[string]any) error {
	if len(changes) == 0 {
		return errors.New("変更がありません")
	}
	for k, v := range changes {
		if !slices.Contains(Fields, k) {
			return fmt.Errorf("unsupported field %q", k)
		}
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s must be string", k)
		}
		switch k {
		case "color":
			if !slices.Contains(Colors, s) {
				return fmt.Errorf("色は %s のどれかです: %q", strings.Join(Colors, " / "), s)
			}
		case "start_on", "due_on":
			if s == "" {
				continue
			}
			if _, err := time.Parse(time.DateOnly, s); err != nil {
				return fmt.Errorf("%s は YYYY-MM-DD で書いてください: %q", k, s)
			}
		}
	}
	return nil
}

// CheckDates checks a project's dates after an edit: a start date needs a due date (Asana
// requires it), and does not come after it. Both are YYYY-MM-DD or "".
func CheckDates(startOn, dueOn string) error {
	if startOn == "" {
		return nil
	}
	if dueOn == "" {
		return errors.New("開始日を付けるには期日も必要です")
	}
	if startOn > dueOn {
		return fmt.Errorf("開始日 (%s) が期日 (%s) より後です", startOn, dueOn)
	}
	return nil
}

// FieldLabel is the label of an update_project field ("" for unknown keys).
func FieldLabel(k string) string {
	return map[string]string{"notes": "説明", "html_notes": "説明", "owner": "オーナー", "start_on": "開始日",
		"due_on": "期日", "color": "色"}[k]
}
