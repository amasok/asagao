package store

import "testing"

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
	// ResetReason is stored with each reset discard, and IsResetDiscard reads it back.
	if ResetReason != "ローカルのデータを初期化したため、送らずに破棄しました" || !IsResetDiscard(Discard{Reason: "ローカルのデータを初期化したため、送らずに破棄しました"}) {
		t.Errorf("ResetReason = %q", ResetReason)
	}
	if MaxModifiedWithinDays != 36500 {
		t.Errorf("MaxModifiedWithinDays = %d", MaxModifiedWithinDays)
	}
}
