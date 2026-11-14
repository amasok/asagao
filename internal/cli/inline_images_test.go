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
	// herdr's real environment inside Ghostty: no TMUX, TERM_PROGRAM names herdr.
	herdr := []string{"TERM_PROGRAM", "herdr", "TERM_PROGRAM_VERSION", "0.9.5", "TERM", "xterm-256color", "HERDR_ENV", "1", "GHOSTTY_RESOURCES_DIR", "/g"}
	// A shell inside herdr that set TERM_PROGRAM back to ghostty.
	herdrNested := []string{"TERM_PROGRAM", "ghostty", "TERM", "xterm-ghostty", "HERDR_ENV", "1", "GHOSTTY_RESOURCES_DIR", "/g"}
	for _, c := range []struct {
		name, setting, protocol string
		env                     []string
		want                    bool
	}{
		{"ghostty auto", "auto", "auto", ghostty, true},
		{"ghostty kitty", "", "kitty", ghostty, true},
		{"kitty", "auto", "auto", []string{"TERM", "xterm-kitty", "KITTY_WINDOW_ID", "1"}, true},
		// herdr in Ghostty draws Unicode placeholders (confirmed by the user).
		{"herdr kitty", "auto", "kitty", herdr, true},
		{"herdr forced off", "off", "kitty", herdr, false},
		{"herdr auto protocol", "auto", "auto", herdr, true},
		{"herdr nested shell", "auto", "kitty", herdrNested, true},
		{"herdr without HERDR_ENV", "auto", "kitty", []string{"TERM_PROGRAM", "herdr", "TERM", "xterm-256color", "GHOSTTY_RESOURCES_DIR", "/g"}, true},
		// herdr teams panes set TMUX to a fake socket; still herdr.
		{"herdr teams pane", "auto", "kitty", append([]string{"TMUX", "/tmp/herdr-teams/tmux-fake,0,0"}, herdr...), true},
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
