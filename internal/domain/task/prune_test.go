package task

import "testing"

func TestMayPrune(t *testing.T) {
	for _, tc := range []struct {
		name     string
		c        PruneCandidate
		prune    bool
		remember bool
	}{
		{"not returned", PruneCandidate{}, true, false},
		{"returned", PruneCandidate{Returned: true}, false, false},
		{"queued", PruneCandidate{Queued: true}, false, false},
		{"returned and queued", PruneCandidate{Returned: true, Queued: true}, false, false},
		{"subtask of a returned parent", PruneCandidate{HasParent: true, ParentReturned: true}, false, false},
		{"subtask of a parent not returned", PruneCandidate{HasParent: true}, true, false},
		{"queued subtask of a parent not returned", PruneCandidate{HasParent: true, Queued: true}, false, false},
		{"returned subtask", PruneCandidate{Returned: true, HasParent: true}, false, false},
		{"ParentReturned without a parent does not count", PruneCandidate{ParentReturned: true}, true, false},
		{"linked, not returned", PruneCandidate{Linked: true}, true, true},
		{"linked and returned", PruneCandidate{Linked: true, Returned: true}, false, false},
		{"linked and queued", PruneCandidate{Linked: true, Queued: true}, false, false},
		{"linked subtask of a returned parent", PruneCandidate{Linked: true, HasParent: true, ParentReturned: true}, false, true}, // recorded, though kept
		{"linked subtask of a parent not returned", PruneCandidate{Linked: true, HasParent: true}, true, true},
	} {
		if got := MayPrune(tc.c); got != tc.prune {
			t.Errorf("%s: MayPrune = %v, want %v", tc.name, got, tc.prune)
		}
		if got := RememberRef(tc.c); got != tc.remember {
			t.Errorf("%s: RememberRef = %v, want %v", tc.name, got, tc.remember)
		}
	}
}
