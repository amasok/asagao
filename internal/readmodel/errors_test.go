package readmodel

import (
	"errors"
	"fmt"
	"testing"
)

// The messages are shown to the user (and matched by the entry points' tests).
func TestErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrNotFound, "not found in local store"},
		{ErrAmbiguous, "ambiguous name"},
		{ErrPushBusy, "別のプロセスが未送信の変更を送信中です"},
		{ErrUnsentChanges, "Asana に送っていない変更があります"},
		{ErrSectionWithoutProject, "section requires project"},
	} {
		if tc.err.Error() != tc.want {
			t.Errorf("%q, want %q", tc.err.Error(), tc.want)
		}
	}
}

func TestFilterError(t *testing.T) {
	inner := errors.New("bad date")
	for _, tc := range []struct {
		name   string
		err    *FilterError
		msg    string
		target error
	}{
		{"wrapped", &FilterError{Field: "due_before", Err: inner}, "due_before: bad date", inner},
		{"section", &FilterError{Field: "section", Err: ErrSectionWithoutProject}, "section: section requires project", ErrSectionWithoutProject},
		{"wrapped twice", &FilterError{Field: "assignee", Err: fmt.Errorf("user: %w", ErrNotFound)}, "assignee: user: not found in local store", ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.msg {
				t.Errorf("Error() = %q, want %q", got, tc.msg)
			}
			if !errors.Is(tc.err, tc.target) {
				t.Errorf("errors.Is(%v, %v) = false", tc.err, tc.target)
			}
			var fe *FilterError
			if !errors.As(fmt.Errorf("list: %w", tc.err), &fe) || fe != tc.err {
				t.Errorf("errors.As did not find the FilterError")
			}
		})
	}
}
