package tag

import (
	"slices"
	"testing"
)

func TestNormalizeColor(t *testing.T) {
	for in, want := range map[string]string{"dark-green": "dark-green", " Light-Blue ": "light-blue", "none": "", "": ""} {
		got, err := NormalizeColor(in)
		if err != nil || got != want {
			t.Errorf("NormalizeColor(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"green", "dark-green;", "red\x1b[31m"} {
		if _, err := NormalizeColor(bad); err == nil {
			t.Errorf("NormalizeColor(%q) accepted", bad)
		}
	}
}

// The colors as the real Asana takes and answers them (scripts/realcheck, #31).
func TestColorsAsAsanaTakesThem(t *testing.T) {
	if slices.Contains(Colors, "light-brown") || !slices.Contains(Colors, "light-yellow") {
		t.Fatalf("Colors = %v: Asana refuses light-brown and takes light-yellow", Colors)
	}
	for in, want := range map[string]string{
		"dark-red": "red", "DARK-RED": "red", "red": "red", "dark-blue": "blue", "light-blue": "blue",
		"light-yellow": "yellow", "dark-brown": "yellow", "none": "", "": "", " None ": "",
	} {
		if got := CanonicalColor(in); got != want {
			t.Errorf("CanonicalColor(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range Colors {
		if CanonicalColor(c) == c {
			t.Errorf("%s has no new name", c)
		}
	}
	if _, err := NormalizeColor("light-brown"); err == nil {
		t.Error("light-brown accepted")
	}
}
