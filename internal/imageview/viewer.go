package imageview

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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

	opened := map[int]bool{}
	external := func(i int) error { // once per image, not on every redraw
		if opened[i] {
			return nil
		}
		opened[i] = true
		return OpenExternal(paths[i])
	}
	browse(bufio.NewReader(in), len(paths),
		func(i int) { draw(out, paths, i, protocol, terminalBox(), external) },
		func(i int) { _ = OpenExternal(paths[i]) })
	return nil
}

// browse runs the key loop over n images: show(i) draws image i, open(i) opens it in the OS
// viewer. It returns when the user quits or input ends.
func browse(keys *bufio.Reader, n int, show, open func(i int)) {
	i := 0
	for {
		show(i)
		switch readKey(keys) {
		case "q", "esc", "ctrl+c":
			return
		case "n", "right", "space", "j":
			i = (i + 1) % n
		case "p", "left", "k":
			i = (i - 1 + n) % n
		case "o":
			open(i)
		}
	}
}

// draw shows image i of paths in the terminal box b, with a caption on the last line.
// An image whose size cannot be checked is not drawn but handed to external (the OS
// viewer, which marks it as downloaded first).
func draw(out io.Writer, paths []string, i int, protocol string, b Box, external func(i int) error) {
	Clear(out, protocol)
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	b.Rows = max(1, b.Rows-2) // leave room for the caption
	data, err := os.ReadFile(paths[i])
	if err == nil {
		err = Write(out, data, protocol, b)
	}
	caption := fmt.Sprintf(" %s  (%d/%d)   n/→ 次  p/← 前  o 外部ビューアで開く  q 戻る", displayName(paths[i]), i+1, len(paths))
	switch {
	case errors.Is(err, ErrUnknownSize) && external != nil:
		if oerr := external(i); oerr != nil {
			caption = fmt.Sprintf(" %s: %v（外部ビューアで開けませんでした: %v）  q 戻る", displayName(paths[i]), err, oerr)
		} else {
			caption = fmt.Sprintf(" %s: 端末に表示できない形式なので、外部ビューアで開きました  (%d/%d)   n/→ 次  p/← 前  q 戻る", displayName(paths[i]), i+1, len(paths))
		}
	case err != nil:
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
