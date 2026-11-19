package tag

import "testing"

// #52 attack: every written and every new name is accepted in any case and padding, the
// result is a written name that is stable and keeps the color Asana would show.
func TestAttackNewColorNamesRoundTrip(t *testing.T) {
	var names []string
	names = append(names, Colors...)
	names = append(names, NewColors()...)
	for _, n := range names {
		for _, in := range []string{n, "  " + n + "\t", upper(n)} {
			w, err := NormalizeColor(in)
			if err != nil {
				t.Fatalf("%q: %v", in, err)
			}
			if w2, err := NormalizeColor(w); err != nil || w2 != w {
				t.Errorf("%q -> %q is not stable: %q, %v", in, w, w2, err)
			}
			if got, want := CanonicalColor(w), CanonicalColor(n); got != want {
				t.Errorf("%q -> %q shows as %q, want %q", in, w, got, want)
			}
			found := false
			for _, c := range Colors {
				found = found || c == w
			}
			if !found {
				t.Errorf("%q -> %q is not a color Asana writes", in, w)
			}
		}
	}
	for _, bad := range []string{"light-brown", "dark-yellow", "RED-", "re d", "dark_red", "reds", "рed"} {
		if w, err := NormalizeColor(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, w)
		}
	}
	if len(NewColors()) != 15 {
		t.Errorf("NewColors = %v", NewColors())
	}
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}
