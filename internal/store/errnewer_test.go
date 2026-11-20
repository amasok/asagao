package store

import (
	"strings"
	"testing"
)

// A store written by a newer asagao cannot be opened: the error tells the user what to do
// next, not only what went wrong.
func TestErrNewerSchemaSaysWhatToDo(t *testing.T) {
	if msg := ErrNewerSchema.Error(); !strings.Contains(msg, "asagao を最新の版に更新してください") {
		t.Errorf("ErrNewerSchema = %q, want it to say to update asagao", msg)
	}
}
