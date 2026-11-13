package imageview

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Inline images inside a TUI use kitty's "Unicode placeholders": the image is sent once as a
// virtual placement, and then drawn wherever text cells contain U+10EEEE with diacritics
// for row/column and the image id as foreground color. Because the placeholders are plain
// text, they work inside a cell-based renderer like Bubble Tea.
// See https://sw.kovidgoyal.net/kitty/graphics-protocol/#unicode-placeholders

const placeholder = '\U0010EEEE'

// rowColumnDiacritics are the first entries of kitty's rowcolumn-diacritics.txt;
// index i encodes row/column number i.
var rowColumnDiacritics = []rune{
	0x0305, 0x030D, 0x030E, 0x0310, 0x0312, 0x033D, 0x033E, 0x033F, 0x0346, 0x034A,
	0x034B, 0x034C, 0x0350, 0x0351, 0x0352, 0x0357, 0x035B, 0x0363, 0x0364, 0x0365,
	0x0366, 0x0367, 0x0368, 0x0369, 0x036A, 0x036B, 0x036C, 0x036D, 0x036E, 0x036F,
	0x0483, 0x0484, 0x0485, 0x0486, 0x0487, 0x0592, 0x0593, 0x0594, 0x0595, 0x0597,
	0x0598, 0x0599, 0x059C, 0x059D, 0x059E, 0x059F, 0x05A0, 0x05A1, 0x05A8, 0x05A9,
	0x05AB, 0x05AC, 0x05AF, 0x05C4, 0x0610, 0x0611, 0x0612, 0x0613, 0x0614, 0x0615,
	0x0616, 0x0617, 0x0657, 0x0658, 0x0659, 0x065A, 0x065B, 0x065D, 0x065E, 0x06D6,
	0x06D7, 0x06D8, 0x06D9, 0x06DA, 0x06DB, 0x06DC, 0x06DF, 0x06E0, 0x06E1, 0x06E2,
	0x06E4, 0x06E7, 0x06E8, 0x06EB, 0x06EC, 0x0730, 0x0732, 0x0733, 0x0735, 0x0736,
	0x073A, 0x073D, 0x073F, 0x0740, 0x0741, 0x0743, 0x0745, 0x0747, 0x0749, 0x074A,
}

// MaxPlaceholderCells is the largest number of rows/columns an inline image can span.
var MaxPlaceholderCells = len(rowColumnDiacritics)

// KittyVirtual returns the escape sequence that transmits a PNG as image id (1-255) with a
// virtual placement of cols×rows cells. Sending it again with the same id replaces it.
func KittyVirtual(id int, pngData []byte, cols, rows int) string {
	var b strings.Builder
	enc := base64.StdEncoding.EncodeToString(pngData)
	const chunk = 4096
	for i := 0; i < len(enc); i += chunk {
		end := min(i+chunk, len(enc))
		more := 0
		if end < len(enc) {
			more = 1
		}
		if i == 0 {
			fmt.Fprintf(&b, "\x1b_Ga=T,U=1,f=100,q=2,i=%d,c=%d,r=%d,m=%d;%s\x1b\\", id, cols, rows, more, enc[i:end])
		} else {
			fmt.Fprintf(&b, "\x1b_Gm=%d;%s\x1b\\", more, enc[i:end])
		}
	}
	return b.String()
}

// KittyDelete removes an image and its placements.
func KittyDelete(id int) string { return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", id) }

// PlaceholderLines returns rows lines of cols placeholder cells for image id (1-255).
func PlaceholderLines(id, cols, rows int) []string {
	cols, rows = min(cols, MaxPlaceholderCells), min(rows, MaxPlaceholderCells)
	lines := make([]string, rows)
	for r := range rows {
		var b strings.Builder
		fmt.Fprintf(&b, "\x1b[38;5;%dm", id)
		for c := range cols {
			b.WriteRune(placeholder)
			b.WriteRune(rowColumnDiacritics[r])
			b.WriteRune(rowColumnDiacritics[c])
		}
		b.WriteString("\x1b[39m")
		lines[r] = b.String()
	}
	return lines
}

// ToPNG decodes an image file (PNG, JPEG or GIF) and returns PNG bytes and its size in pixels.
func ToPNG(data []byte) ([]byte, int, int, error) {
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, err
	}
	b := img.Bounds()
	if format == "png" {
		return data, b.Dx(), b.Dy(), nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), b.Dx(), b.Dy(), nil
}

// CellSize returns the terminal cell size in pixels, or 0, 0 if unknown.
func CellSize() (int, int) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || ws.Col == 0 || ws.Row == 0 || ws.Xpixel == 0 || ws.Ypixel == 0 {
		return 0, 0
	}
	return int(ws.Xpixel) / int(ws.Col), int(ws.Ypixel) / int(ws.Row)
}
