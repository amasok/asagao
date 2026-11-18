// Package project holds the rules for changing a project's fields offline (the
// update_project outbox entry): which fields, which values.
package project

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/amasok/asagao/internal/domain/tag"
)

// Fields are the project fields a local edit may change (the keys of an update_project
// payload). Every value is a string; "" clears owner, start_on and due_on.
var Fields = []string{"notes", "html_notes", "owner", "start_on", "due_on", "color"}

// Colors are the colors Asana accepts for a project in a write ("none" for no color):
// the tag colors (tag.Colors).
var Colors = append(slices.Clone(tag.Colors), "none")

// NormalizeColor checks a project color the user typed: one of Colors, or a name Asana's
// GET answers (red, blue, ...: tag.NewColors), in any case. It returns the name to send
// (a new name becomes its written name, tag.WriteColor). "" is not a color.
func NormalizeColor(s string) (string, error) {
	c := strings.ToLower(strings.TrimSpace(s))
	if c == "none" {
		return c, nil
	}
	if w, ok := tag.WriteColor(c); ok {
		return w, nil
	}
	return "", fmt.Errorf("色は %s のどれかです（Asana の新しい名前 %s も使えます）: %q",
		strings.Join(Colors, " / "), strings.Join(tag.NewColors(), " / "), s)
}

// NormalizeFields checks the changes (ValidateFields) and returns a copy with the color
// as it is sent (NormalizeColor).
func NormalizeFields(changes map[string]any) (map[string]any, error) {
	if err := ValidateFields(changes); err != nil {
		return nil, err
	}
	out := maps.Clone(changes)
	if c, ok := out["color"].(string); ok {
		out["color"], _ = NormalizeColor(c)
	}
	return out, nil
}

// ValidateFields checks the field changes of a local edit: at least one, each a field that
// can be changed, with a string value, a color Asana has (NormalizeColor), and dates as YYYY-MM-DD.
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
			if _, err := NormalizeColor(s); err != nil {
				return err
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
