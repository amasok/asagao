package cli

import "testing"

func TestInlineImagesEnabled(t *testing.T) {
	env := func(kv ...string) func(string) string {
		m := map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return func(k string) string { return m[k] }
	}
	ghostty := []string{"TERM_PROGRAM", "ghostty", "TERM", "xterm-ghostty", "GHOSTTY_RESOURCES_DIR", "/g"}
	herdr := []string{"TERM_PROGRAM", "herdr", "TMUX", "/tmp/herdr-teams-x/tmux-fake,0,0", "GHOSTTY_RESOURCES_DIR", "/g", "TERM", "xterm-ghostty"}
	for _, c := range []struct {
		name, setting, protocol string
		env                     []string
		want                    bool
	}{
		{"ghostty auto", "auto", "auto", ghostty, true},
		{"ghostty kitty", "", "kitty", ghostty, true},
		{"kitty", "auto", "auto", []string{"TERM", "xterm-kitty", "KITTY_WINDOW_ID", "1"}, true},
		// The reported case: Ghostty → herdr with image_protocol = "kitty". herdr shows kitty
		// images but leaves Unicode placeholders blank, so auto must not use them.
		{"herdr kitty", "auto", "kitty", herdr, false},
		{"herdr forced on", "on", "kitty", herdr, true},
		{"tmux in ghostty", "auto", "kitty", []string{"TMUX", "/tmp/tmux", "TERM", "tmux-256color", "GHOSTTY_RESOURCES_DIR", "/g"}, false},
		{"zellij", "auto", "kitty", append([]string{"ZELLIJ", "0"}, ghostty...), false},
		{"iterm protocol", "auto", "iterm", ghostty, false},
		{"off", "off", "kitty", ghostty, false},
		{"iTerm2", "auto", "auto", []string{"TERM_PROGRAM", "iTerm.app"}, false},
	} {
		if got := inlineImagesEnabled(c.setting, c.protocol, env(c.env...)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
