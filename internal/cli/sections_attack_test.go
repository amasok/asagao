package cli

import (
	"strings"
	"testing"
)

// Deleting needs --yes: without it nothing is queued or sent, and the error says what to do.
func TestAttackRmWithoutYesChangesNothing(t *testing.T) {
	for _, args := range [][]string{
		{"section", "rm", "完了", "--project", "新居"},
		{"tag", "rm", "急ぎ"},
	} {
		path, requests, run := dryRunEnv(t)
		before := storeState(t, path)
		out, errOut, err := run(args...)
		if err == nil || !strings.Contains(err.Error(), "--yes") {
			t.Errorf("%v: err = %v\n%s%s", args, err, out, errOut)
		}
		if requests.Load() != 0 {
			t.Errorf("%v: %d requests", args, requests.Load())
		}
		if after := storeState(t, path); after != before {
			t.Errorf("%v: store changed", args)
		}
	}
}

// Bad input: unknown names, both --before and --after, an empty name, an unknown color.
func TestAttackSectionTagBadInput(t *testing.T) {
	path, requests, run := dryRunEnv(t)
	before := storeState(t, path)
	for _, args := range [][]string{
		{"section", "create", "", "--project", "新居"},
		{"section", "create", "   ", "--project", "新居"},
		{"section", "create", "x", "--project", "ない"},
		{"section", "create", "x", "--project", "新居", "--before", "家具", "--after", "完了"},
		{"section", "create", "x", "--project", "新居", "--before", "ない"},
		{"section", "move", "完了", "--project", "新居"},
		{"section", "move", "完了", "--project", "新居", "--before", "完了"},
		{"section", "rm", "ない", "--project", "新居", "--yes"},
		{"tag", "rename", "ない", "x"},
		{"tag", "rename", "急ぎ", ""},
		{"tag", "color", "急ぎ", "chartreuse-ish"},
		{"tag", "rm", "ない", "--yes"},
	} {
		out, errOut, err := run(args...)
		if err == nil {
			t.Errorf("%v: no error\n%s%s", args, out, errOut)
		}
	}
	if requests.Load() != 0 {
		t.Errorf("%d requests", requests.Load())
	}
	if after := storeState(t, path); after != before {
		t.Errorf("the store changed:\nbefore %s\nafter  %s", before, after)
	}
}
