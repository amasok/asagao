package store

import (
	"testing"

	"github.com/amasok/asagao/internal/readmodel"
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
