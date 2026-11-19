package tag

import (
	"slices"
	"testing"
)

func TestNormalizeColor(t *testing.T) {
	for in, want := range map[string]string{"dark-green": "dark-green", " Light-Blue ": "light-blue", "none": "", "": "",
		// The names Asana's GET answers become the written name (#52); a name two colors
		// share is the first of them in Colors.
		"green": "dark-green", " RED ": "dark-red", "blue": "dark-blue", "cool-gray": "dark-warm-gray",
		"yellow": "dark-brown", "hot-pink": "dark-pink", "purple": "light-purple", "pink": "light-red"} {
		got, err := NormalizeColor(in)
		if err != nil || got != want {
			t.Errorf("NormalizeColor(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"chartreuse", "dark-green;", "red\x1b[31m", "light-brown"} {
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

// Every name Asana answers is taken, and sends a written name Asana answers by it (#52).
func TestWriteColorRoundTrips(t *testing.T) {
	news := NewColors()
	if len(news) != 15 || slices.Contains(news, "") {
		t.Fatalf("NewColors() = %v: 18 colors, three names shared by two", news)
	}
	for _, n := range news {
		w, ok := WriteColor(n)
		if !ok || !slices.Contains(Colors, w) || CanonicalColor(w) != n {
			t.Errorf("WriteColor(%q) = %q, %v", n, w, ok)
		}
	}
	for _, c := range Colors {
		if w, ok := WriteColor(c); !ok || w != c {
			t.Errorf("WriteColor(%q) = %q, %v: a written name is kept", c, w, ok)
		}
	}
	for _, bad := range []string{"", "none", "light-brown", "chartreuse"} {
		if w, ok := WriteColor(bad); ok {
			t.Errorf("WriteColor(%q) = %q", bad, w)
		}
	}
}
