package tag

import "testing"

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
