package readmodel

import "testing"

func TestStoredConstants(t *testing.T) {
	for got, want := range map[string]string{
		BriefNone: "none", BriefUnavailable: "unavailable", BriefAvailable: "available",
		TasksWanted: "wanted", TasksOpen: "open", TasksAll: "all",
		CFText: "text", CFNumber: "number", CFEnum: "enum", CFMultiEnum: "multi_enum", CFDate: "date", CFPeople: "people",
	} {
		if got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
	if AnyUnsent != -1 {
		t.Errorf("AnyUnsent = %d", AnyUnsent)
	}
}
