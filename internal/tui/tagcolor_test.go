package tui

import (
	"context"
	"testing"
	"time"

	"github.com/amasok/asagao/internal/readmodel"
)

// The color picker marks the current color and starts on it, whether the cache holds the
// name Asana's GET answers (red, after a pull) or the written one (after a local change);
// a name two colors share is the first of them (#52).
func TestTUITagColorPickerMarksCurrent(t *testing.T) {
	m := New(context.Background(), onStore(fixtureStore(t), Deps{Now: func() time.Time { return testNow }}))
	for color, want := range map[string]string{
		"red": "dark-red", "RED": "dark-red", "dark-red": "dark-red", "blue": "dark-blue", "light-blue": "light-blue",
		"yellow": "dark-brown", "light-yellow": "light-yellow", "cool-gray": "dark-warm-gray", "purple": "light-purple",
		"": "none", "none": "none", "chartreuse": "",
	} {
		m.openTagColors(readmodel.TagCount{GID: "g1", Name: "急ぎ", Color: color})
		p := m.picker
		var marked []string
		for _, it := range p.items {
			if it.hint != "" {
				marked = append(marked, it.value)
			}
		}
		switch {
		case want == "" && len(marked) != 0:
			t.Errorf("%q: marked %v, want none marked", color, marked)
		case want != "" && (len(marked) != 1 || marked[0] != want):
			t.Errorf("%q: marked %v, want [%s]", color, marked, want)
		case want != "" && p.items[p.cursor].value != want:
			t.Errorf("%q: cursor on %q, want %q", color, p.items[p.cursor].value, want)
		}
	}
	m.openTagColors(readmodel.TagCount{GID: "g1", Name: "急ぎ", Color: "red"})
	for _, it := range m.picker.items {
		if it.value == "dark-red" && it.label != "dark-red（red）" {
			t.Errorf("label %q: the name Asana shows is not there", it.label)
		}
	}
}
