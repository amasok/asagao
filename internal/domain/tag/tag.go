// Package tag holds the rules of tags themselves: the colors Asana accepts.
package tag

import (
	"fmt"
	"slices"
	"strings"
)

// Colors are the tag colors Asana accepts in a write, in the order its color picker shows
// them. The real Asana (2026-11, scripts/realcheck #31) takes exactly these and "none";
// "light-brown" is refused with 400, "light-yellow" is taken.
var Colors = []string{
	"dark-pink", "dark-green", "dark-blue", "dark-red", "dark-teal", "dark-brown",
	"dark-orange", "dark-purple", "dark-warm-gray",
	"light-pink", "light-green", "light-blue", "light-red", "light-teal", "light-yellow",
	"light-orange", "light-purple", "light-warm-gray",
}

// renamed is the name Asana's GET answers for each color written (the PUT answer still
// echoes the written name). Measured on the real Asana for tags and projects alike.
// Some colors are now the same: dark-blue and light-blue are blue, dark-warm-gray and
// light-warm-gray are cool-gray, dark-brown and light-yellow are yellow.
var renamed = map[string]string{
	"dark-pink": "hot-pink", "dark-green": "green", "dark-blue": "blue", "dark-red": "red",
	"dark-teal": "aqua", "dark-brown": "yellow", "dark-orange": "orange", "dark-purple": "indigo",
	"dark-warm-gray": "cool-gray", "light-pink": "magenta", "light-green": "yellow-green",
	"light-blue": "blue", "light-red": "pink", "light-teal": "blue-green", "light-yellow": "yellow",
	"light-orange": "yellow-orange", "light-purple": "purple", "light-warm-gray": "cool-gray",
}

// CanonicalColor is the color as Asana's GET names it: a written name becomes its new
// name, and no color ("" or "none") is "". Use it to compare colors (a tag's or a
// project's), never to send one.
func CanonicalColor(c string) string {
	c = strings.ToLower(strings.TrimSpace(c))
	if slices.Contains(NoColor, c) {
		return ""
	}
	if n, ok := renamed[c]; ok {
		return n
	}
	return c
}

// NoColor are the words that mean "no color".
var NoColor = []string{"", "none"}

// WriteColor is the name to send for the color c: c itself when it is one of Colors, or
// the first of Colors that Asana's GET answers as c (red is dark-red; blue, cool-gray and
// yellow, which two colors share, are the one listed first). c is matched in any case.
// It is false for anything else, no color included.
func WriteColor(c string) (string, bool) {
	c = strings.ToLower(strings.TrimSpace(c))
	if slices.Contains(Colors, c) {
		return c, true
	}
	for _, w := range Colors {
		if renamed[w] == c {
			return w, true
		}
	}
	return "", false
}

// NewColors are the names Asana's GET answers (CanonicalColor of Colors), in the order of
// Colors, each once.
func NewColors() []string {
	var out []string
	for _, c := range Colors {
		if n := renamed[c]; !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// NormalizeColor checks a color the user typed: one of Colors or NewColors (any case;
// a new name becomes the written name WriteColor gives), or "" / "none" for no color
// (returned as ""). It returns an error listing the colors otherwise.
func NormalizeColor(c string) (string, error) {
	c = strings.ToLower(strings.TrimSpace(c))
	if slices.Contains(NoColor, c) {
		return "", nil
	}
	if w, ok := WriteColor(c); ok {
		return w, nil
	}
	return "", fmt.Errorf("タグの色 %q は使えません。次のどれかを指定してください: none, %s（Asana の新しい名前 %s も使えます）",
		c, strings.Join(Colors, ", "), strings.Join(NewColors(), ", "))
}
