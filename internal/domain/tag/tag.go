// Package tag holds the rules of tags themselves: the colors Asana accepts.
package tag

import (
	"fmt"
	"slices"
	"strings"
)

// Colors are the tag colors Asana accepts, in the order its color picker shows them.
var Colors = []string{
	"dark-pink", "dark-green", "dark-blue", "dark-red", "dark-teal", "dark-brown",
	"dark-orange", "dark-purple", "dark-warm-gray",
	"light-pink", "light-green", "light-blue", "light-red", "light-teal", "light-brown",
	"light-orange", "light-purple", "light-warm-gray",
}

// NoColor are the words that mean "no color".
var NoColor = []string{"", "none"}

// NormalizeColor checks a color the user typed: one of Colors (any case), or "" / "none"
// for no color (returned as ""). It returns an error listing the colors otherwise.
func NormalizeColor(c string) (string, error) {
	c = strings.ToLower(strings.TrimSpace(c))
	if slices.Contains(NoColor, c) {
		return "", nil
	}
	if slices.Contains(Colors, c) {
		return c, nil
	}
	return "", fmt.Errorf("タグの色 %q は使えません。次のどれかを指定してください: none, %s", c, strings.Join(Colors, ", "))
}
