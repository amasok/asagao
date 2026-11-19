package tag

import (
	"slices"
	"testing"
)

// Every name Asana answers is its own canonical form (comparing twice is stable), and no
// written name is turned into another written name.
func TestCanonicalColorIsIdempotent(t *testing.T) {
	for _, c := range Colors {
		once := CanonicalColor(c)
		if once == "" {
			t.Errorf("%s became no color", c)
		}
		if twice := CanonicalColor(once); twice != once {
			t.Errorf("%s: %q then %q", c, once, twice)
		}
	}
	for k, v := range renamed {
		if _, again := renamed[v]; again {
			t.Errorf("%s → %s is itself renamed", k, v)
		}
	}
}

// Every color offered (Colors) is renamed, and the old names Asana does not take any more
// are not offered; no-color words and odd input do not panic or become colors.
func TestCanonicalColorEdges(t *testing.T) {
	for _, c := range Colors {
		if _, ok := renamed[c]; !ok {
			t.Errorf("%s is offered but has no new name", c)
		}
	}
	for k := range renamed {
		if !slices.Contains(Colors, k) && k != "dark-brown" {
			t.Logf("renamed has %s which is not offered", k)
		}
	}
	for in, want := range map[string]string{
		"\tNONE\n": "", "None": "", "light-brown": "light-brown", "unknown-color": "unknown-color",
		"  Dark-Red  ": "red", "yellow-orange": "yellow-orange", "cool-gray": "cool-gray",
	} {
		if got := CanonicalColor(in); got != want {
			t.Errorf("CanonicalColor(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"light-brown", "LIGHT-BROWN", "chartreuse", "none-ish"} {
		if got, err := NormalizeColor(bad); err == nil && got != "" {
			t.Errorf("NormalizeColor(%q) = %q: not a color Asana takes in a write", bad, got)
		}
	}
	for _, ok := range []string{" Light-Yellow ", "DARK-RED", "none", "", "red", "Yellow"} {
		if _, err := NormalizeColor(ok); err != nil {
			t.Errorf("NormalizeColor(%q): %v", ok, err)
		}
	}
}
