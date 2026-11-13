package imageview

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

// Run shows images full screen one at a time until the user quits.
// Keys: n / → / space = next, p / ← = previous, o = open in the OS viewer, q / esc = back.
func Run(paths []string, protocol string) error {
	if len(paths) == 0 {
		return nil
	}
	protocol = Resolve(protocol)
	if protocol == ProtocolOpen {
		return OpenExternal(paths...)
	}
	in, out := os.Stdin, os.Stdout
	state, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return OpenExternal(paths...)
	}
	defer term.Restore(int(in.Fd()), state)
	fmt.Fprint(out, "\x1b[?1049h\x1b[?25l") // alternate screen, hide cursor
	defer func() {
		Clear(out, protocol)
		fmt.Fprint(out, "\x1b[2J\x1b[?25h\x1b[?1049l")
	}()

	keys := bufio.NewReader(in)
	i := 0
	for {
		draw(paths, i, protocol)
		switch readKey(keys) {
		case "q", "esc", "ctrl+c":
			return nil
		case "n", "right", "space", "j":
			i = (i + 1) % len(paths)
		case "p", "left", "k":
			i = (i - 1 + len(paths)) % len(paths)
		case "o":
			_ = OpenExternal(paths[i])
		}
	}
}

func draw(paths []string, i int, protocol string) {
	out := os.Stdout
	Clear(out, protocol)
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	b := terminalBox()
	b.Rows = max(1, b.Rows-2) // leave room for the caption
	data, err := os.ReadFile(paths[i])
	if err == nil {
		err = Write(out, data, protocol, b)
	}
	caption := fmt.Sprintf(" %s  (%d/%d)   n/→ 次  p/← 前  o 外部ビューアで開く  q 戻る", displayName(paths[i]), i+1, len(paths))
	if err != nil {
		caption = fmt.Sprintf(" %s: %v   o 外部ビューアで開く  q 戻る", displayName(paths[i]), err)
	}
	fmt.Fprintf(out, "\x1b[%d;1H\x1b[2K%s", b.Rows+2, caption)
}

// displayName strips the "<gid>-" prefix added to cached attachment files.
func displayName(path string) string {
	base := filepath.Base(path)
	if i := strings.IndexByte(base, '-'); i > 0 && strings.Trim(base[:i], "0123456789") == "" {
		return base[i+1:]
	}
	return base
}

func terminalBox() Box {
	b := Box{Cols: 80, Rows: 24}
	if ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ); err == nil && ws.Col > 0 {
		b.Cols, b.Rows = int(ws.Col), int(ws.Row)
		if ws.Xpixel > 0 && ws.Ypixel > 0 {
			b.CellW, b.CellH = int(ws.Xpixel)/b.Cols, int(ws.Ypixel)/b.Rows
		}
	}
	return b
}

func readKey(r *bufio.Reader) string {
	c, err := r.ReadByte()
	if err != nil {
		return "q"
	}
	switch c {
	case 3:
		return "ctrl+c"
	case ' ':
		return "space"
	case 0x1b:
		if r.Buffered() == 0 {
			return "esc"
		}
		if next, _ := r.ReadByte(); next == '[' {
			switch code, _ := r.ReadByte(); code {
			case 'C':
				return "right"
			case 'D':
				return "left"
			}
		}
		return ""
	}
	return string(c)
}
