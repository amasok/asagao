package asana

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// A 401 is answered at once (a rejected token does not get better by retrying), carries
// the client's AuthHint, and is recognized through wrapping.
func TestUnauthorizedIsNotRetriedAndCarriesHint(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"errors":[{"message":"Not Authorized"}]}`)
	})
	c.MaxRetries = 3
	c.AuthHint = "asagao auth login --pat で登録し直してください"
	ctx := context.Background()

	_, err := c.Me(ctx)
	if !IsUnauthorized(err) || !IsUnauthorized(fmt.Errorf("wrapped: %w", err)) {
		t.Fatalf("Me = %v, want a 401", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a 401 must not be retried (%d requests)", n)
	}
	if !strings.Contains(err.Error(), "Not Authorized") || !strings.Contains(err.Error(), c.AuthHint) {
		t.Fatalf("Error() = %q, want Asana's message and the hint", err)
	}

	// Writes and uploads (built separately) carry the hint too.
	if _, err := c.UploadAttachment(ctx, "t1", "a.txt", []byte("x")); !IsUnauthorized(err) || !strings.Contains(err.Error(), c.AuthHint) {
		t.Fatalf("UploadAttachment = %v", err)
	}
}

// Only a 401 is "unauthorized"; other errors get no login hint.
func TestHintOnlyOn401(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusPaymentRequired, http.StatusNotFound, http.StatusBadRequest} {
		c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{"errors":[{"message":"nope"}]}`)
		})
		c.AuthHint = "LOGIN-HINT"
		_, err := c.Me(context.Background())
		if err == nil || IsUnauthorized(err) || strings.Contains(err.Error(), "LOGIN-HINT") {
			t.Fatalf("%d: err = %v, want no login hint", status, err)
		}
	}
	if IsUnauthorized(nil) || IsUnauthorized(fmt.Errorf("x")) {
		t.Fatal("IsUnauthorized(non-API error) should be false")
	}
	// 401 without a body: still a readable message.
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	if _, err := c.Me(context.Background()); !IsUnauthorized(err) || !strings.Contains(err.Error(), "Unauthorized") {
		t.Fatalf("401 without a body: %v", err)
	}
}
