package store

import (
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
	"github.com/amasok/asagao/internal/ui/format"
)

// The store's errors are readmodel's values, so errors.Is matches either.
func TestErrorsAreReadmodelValues(t *testing.T) {
	for _, tc := range []struct {
		name      string
		store, rm error
	}{
		{"ErrNotFound", ErrNotFound, readmodel.ErrNotFound},
		{"ErrAmbiguous", ErrAmbiguous, readmodel.ErrAmbiguous},
		{"ErrPushBusy", ErrPushBusy, readmodel.ErrPushBusy},
		{"ErrUnsentChanges", ErrUnsentChanges, readmodel.ErrUnsentChanges},
		{"ErrSectionWithoutProject", ErrSectionWithoutProject, readmodel.ErrSectionWithoutProject},
		{"ErrOpSending", ErrOpSending, readmodel.ErrOpSending},
	} {
		if tc.store != tc.rm {
			t.Errorf("store.%s is not readmodel.%s", tc.name, tc.name)
		}
	}
}

func TestInboxTypesAreReadmodelTypes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		store, rm any
	}{
		{"InboxState", InboxState{}, readmodel.InboxState{}},
		{"Notification", Notification{}, readmodel.Notification{}},
		{"NotificationFilter", NotificationFilter{}, readmodel.NotificationFilter{}},
		{"FilterSpec", FilterSpec{}, readmodel.FilterSpec{}},
		{"FilterError", &FilterError{}, &readmodel.FilterError{}},
	} {
		if typeName(tc.store) != typeName(tc.rm) {
			t.Errorf("store.%s is %s, want %s", tc.name, typeName(tc.store), typeName(tc.rm))
		}
	}
	if InboxOK != readmodel.InboxOK || InboxForbidden != readmodel.InboxForbidden || InboxPayment != readmodel.InboxPayment ||
		InboxError != readmodel.InboxError || MaxModifiedWithinDays != readmodel.MaxModifiedWithinDays {
		t.Error("the store's constants differ from readmodel's")
	}
}

// The descriptions of outbox entries are format's: the store's names are its values.
func TestDescriptionsAreReadmodels(t *testing.T) {
	if typeName(Discard{}) != typeName(readmodel.Discard{}) {
		t.Errorf("store.Discard is %s", typeName(Discard{}))
	}
	if VerifyNote != format.VerifyNote || DiscardNotice != format.DiscardNotice || ResetReason != format.ResetReason {
		t.Error("the store's constants differ from format's")
	}
	if len(StatusTypes) != len(format.StatusTypes) || &StatusTypes[0] != &format.StatusTypes[0] {
		t.Error("store.StatusTypes is not format.StatusTypes")
	}
	d := Discard{Kind: OpAddComment, Payload: map[string]any{"text": "a", "html_text": "<b>a</b>"}}
	got := DiscardDetails(d, func(string) string { return "**a**" })
	want := format.DiscardDetails(d, func(string) string { return "**a**" })
	if len(got) != 1 || len(want) != 1 || got[0].Label != want[0].Label || got[0].Text != want[0].Text {
		t.Errorf("DiscardDetails = %+v, want %+v", got, want)
	}
	if DiscardDetails(Discard{Kind: OpAddTag}, nil) != nil {
		t.Error("DiscardDetails of nothing is not nil")
	}
}
