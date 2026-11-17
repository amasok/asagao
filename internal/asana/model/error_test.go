package model

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestError(t *testing.T) {
	withMessage := func(status int, msg, hint string) *Error {
		e := &Error{Status: status, Hint: hint}
		m, _ := json.Marshal(msg)
		if err := json.Unmarshal([]byte(`{"errors":[{"message":`+string(m)+`}]}`), e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	for _, tc := range []struct {
		err                 *Error
		want                string
		unauthorized, payme bool
	}{
		{&Error{Status: 404}, "asana: 404 Not Found", false, false},
		{&Error{Status: 401}, "asana: 401 Unauthorized", true, false},
		{&Error{Status: 401, Hint: "ログインし直してください"}, "asana: 401 Unauthorized（ログインし直してください）", true, false},
		{&Error{Status: 402}, "asana: 402 Payment Required", false, true},
		{withMessage(400, "name: Missing input", ""), "asana: 400 name: Missing input", false, false},
		{withMessage(400, "bad\x1b[31m red", ""), "asana: 400 bad[31m red", false, false}, // the message goes through termsafe
		{withMessage(403, "Forbidden here", "hint"), "asana: 403 Forbidden here（hint）", false, false},
		{&Error{Status: 599}, "asana: 599 ", false, false},
	} {
		if got := tc.err.Error(); got != tc.want {
			t.Errorf("Error() = %q, want %q", got, tc.want)
		}
		wrapped := fmt.Errorf("sync: %w", tc.err)
		if IsUnauthorized(wrapped) != tc.unauthorized || IsPaymentRequired(wrapped) != tc.payme {
			t.Errorf("%d: IsUnauthorized = %v, IsPaymentRequired = %v", tc.err.Status, IsUnauthorized(wrapped), IsPaymentRequired(wrapped))
		}
	}
	if IsUnauthorized(nil) || IsPaymentRequired(fmt.Errorf("401")) {
		t.Error("not an *Error")
	}
}

func TestCustomFieldSubtype(t *testing.T) {
	for _, tc := range []struct {
		f    CustomField
		want string
	}{
		{CustomField{ResourceSubtype: "enum", Type: "text"}, "enum"},
		{CustomField{Type: "number"}, "number"},
		{CustomField{}, ""},
	} {
		if got := tc.f.Subtype(); got != tc.want {
			t.Errorf("%+v.Subtype() = %q, want %q", tc.f, got, tc.want)
		}
	}
}

func TestSyncToken(t *testing.T) {
	for _, tc := range []struct {
		err   error
		token string
		ok    bool
	}{
		{&Error{Status: 412, Sync: "tok"}, "tok", true},
		{fmt.Errorf("events: %w", &Error{Status: 412, Sync: "tok"}), "tok", true},
		{&Error{Status: 412}, "", false},              // no token in the answer
		{&Error{Status: 400, Sync: "tok"}, "", false}, // not a 412
		{fmt.Errorf("412"), "", false},
		{nil, "", false},
	} {
		if token, ok := SyncToken(tc.err); token != tc.token || ok != tc.ok {
			t.Errorf("SyncToken(%v) = %q, %v; want %q, %v", tc.err, token, ok, tc.token, tc.ok)
		}
	}
}
