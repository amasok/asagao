package store

import (
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
)

// The sync_state keys and values of the inbox state are in users' databases: they must not
// change with where the constants are declared.
func TestInboxStoredKeysAndValuesUnchanged(t *testing.T) {
	for got, want := range map[string]string{
		KeyInboxStatus: "inbox_status", KeyInboxError: "inbox_error", KeyInboxWrite: "inbox_write",
		InboxOK: "ok", InboxForbidden: "forbidden", InboxPayment: "payment", InboxError: "error",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	// The reason recorded for a reset discard is stored, and IsReset reads it back.
	if readmodel.DiscardReasonReset != "reset" || !(Discard{Reason: "reset"}).IsReset() {
		t.Errorf("DiscardReasonReset = %q", readmodel.DiscardReasonReset)
	}
	if MaxModifiedWithinDays != 36500 {
		t.Errorf("MaxModifiedWithinDays = %d", MaxModifiedWithinDays)
	}
}
