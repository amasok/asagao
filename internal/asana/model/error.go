package model

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/amasok/asagao/internal/termsafe"
)

// Error is an Asana API error response.
type Error struct {
	Status int
	// Hint is what the user can do about it (set on 401 from Client.AuthHint).
	Hint   string `json:"-"`
	Errors []struct {
		Message string `json:"message"`
		Help    string `json:"help"`
	} `json:"errors"`
	// Sync is the new sync token of a 412 from GET /events (see SyncToken).
	Sync string `json:"sync"`
}

func (e *Error) Error() string {
	msg := http.StatusText(e.Status)
	if len(e.Errors) > 0 {
		msg = termsafe.String(e.Errors[0].Message) // shown in the TUI status line as is
	}
	if e.Hint != "" {
		return fmt.Sprintf("asana: %d %s（%s）", e.Status, msg, e.Hint)
	}
	return fmt.Sprintf("asana: %d %s", e.Status, msg)
}

// IsUnauthorized reports whether Asana rejected the token (401: revoked or expired PAT,
// revoked OAuth grant).
func IsUnauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusUnauthorized
}

// IsPaymentRequired reports whether the endpoint needs a paid Asana plan.
func IsPaymentRequired(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusPaymentRequired
}
