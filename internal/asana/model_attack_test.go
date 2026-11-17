package asana

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Asana's error text reaches a terminal: control characters are stripped, a hint is added
// in full-width brackets, and the same *Error is found whichever package names the type.
func TestErrorTextAndMatching(t *testing.T) {
	var e Error
	if err := json.Unmarshal([]byte(`{"errors":[{"message":"bad\u001b[2J name","help":"h"}],"sync":"tok"}`), &e); err != nil {
		t.Fatal(err)
	}
	e.Status = 401
	e.Hint = "ログインしてください"
	if got := e.Error(); strings.ContainsRune(got, 0x1b) || !strings.HasSuffix(got, "（ログインしてください）") || !strings.HasPrefix(got, "asana: 401 ") {
		t.Errorf("Error() = %q", got)
	}
	if e.Hint != "" && json.Valid([]byte(`{}`)) { // Hint is not part of the JSON
		b, _ := json.Marshal(&e)
		if strings.Contains(string(b), "ログイン") {
			t.Errorf("Hint is in the JSON: %s", b)
		}
	}
	wrapped := fmt.Errorf("a: %w", fmt.Errorf("b: %w", &e))
	var as *Error
	if !errors.As(wrapped, &as) || as != &e || !IsUnauthorized(wrapped) || IsPaymentRequired(wrapped) {
		t.Errorf("matching failed for %v", wrapped)
	}
	if _, ok := SyncToken(wrapped); ok { // only a 412 carries a sync token
		t.Error("SyncToken of a 401")
	}
	stale := fmt.Errorf("x: %w", &Error{Status: 412, Sync: "tok"})
	if tok, ok := SyncToken(stale); !ok || tok != "tok" {
		t.Errorf("SyncToken = %q, %v", tok, ok)
	}
	if IsUnauthorized(nil) || IsPaymentRequired(errors.New("402")) {
		t.Error("false positives")
	}
	if got := (&Error{Status: 999}).Error(); got != "asana: 999 " {
		t.Errorf("unknown status text = %q", got)
	}
}

// Partial is not decoded from or written to JSON.
func TestTaskPartialNotInJSON(t *testing.T) {
	var task Task
	if err := json.Unmarshal([]byte(`{"gid":"1","Partial":true,"partial":true}`), &task); err != nil {
		t.Fatal(err)
	}
	task.Partial = true
	b, _ := json.Marshal(task)
	if task.GID != "1" || strings.Contains(strings.ToLower(string(b)), "partial") {
		t.Errorf("Partial leaks: %s", b)
	}
	var back Task
	_ = json.Unmarshal([]byte(`{"gid":"1","Partial":true}`), &back)
	if back.Partial {
		t.Error("Partial was decoded")
	}
}
