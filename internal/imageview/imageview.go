// Package imageview shows images in the terminal.
//
// Supported terminal protocols:
//   - iTerm2 inline images (iTerm2, WezTerm): any format the terminal can decode
//   - kitty graphics protocol (kitty, Ghostty): PNG, JPEG and GIF (converted to PNG)
//
// Other terminals (and tmux, which needs passthrough) fall back to the OS image viewer.
package imageview

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	ProtocolAuto  = "auto"
	ProtocolITerm = "iterm"
	ProtocolKitty = "kitty"
	ProtocolOpen  = "open" // OS image viewer
)

// Detect picks a protocol from the environment (getenv is os.Getenv in production).
//
// Multiplexers (tmux, zellij, …) sit between the program and the real terminal and usually
// do not pass inline images through, so they get the OS viewer. herdr does pass the kitty
// protocol through. Variables such as
// GHOSTTY_RESOURCES_DIR are inherited by programs inside a multiplexer, so they are trusted
// only when TERM_PROGRAM does not name some other program. Set ui.image_protocol to
// override the detection.
func Detect(getenv func(string) string) string {
	if getenv("TMUX") != "" || getenv("ZELLIJ") != "" {
		return ProtocolOpen
	}
	program := getenv("TERM_PROGRAM")
	switch program {
	case "herdr": // herdr passes the kitty graphics protocol through
		return ProtocolKitty
	case "iTerm.app", "WezTerm":
		return ProtocolITerm
	case "ghostty":
		return ProtocolKitty
	case "":
	default:
		return ProtocolOpen // some other terminal or a multiplexer
	}
	if getenv("KITTY_WINDOW_ID") != "" || strings.Contains(getenv("TERM"), "kitty") || getenv("GHOSTTY_RESOURCES_DIR") != "" {
		return ProtocolKitty
	}
	if getenv("LC_TERMINAL") == "iTerm2" {
		return ProtocolITerm
	}
	return ProtocolOpen
}

// PlaceholdersSupported reports whether the terminal can surely draw kitty Unicode
// placeholders (images placed inside text): kitty or Ghostty, not inside a multiplexer.
// Multiplexers such as herdr may pass ordinary kitty images through (so the full-screen
// viewer works) but not virtual placements, which then show as blank cells.
func PlaceholdersSupported(getenv func(string) string) bool {
	if getenv("TMUX") != "" || getenv("ZELLIJ") != "" || getenv("STY") != "" {
		return false
	}
	term := getenv("TERM")
	if strings.HasPrefix(term, "tmux") || strings.HasPrefix(term, "screen") {
		return false
	}
	switch getenv("TERM_PROGRAM") {
	case "ghostty":
		return true
	case "":
		return getenv("KITTY_WINDOW_ID") != "" || term == "xterm-kitty" || term == "xterm-ghostty"
	}
	return false // some other terminal, or a multiplexer such as herdr
}

// Resolve turns "auto" (or "") into a concrete protocol.
func Resolve(protocol string) string {
	if protocol == "" || protocol == ProtocolAuto {
		return Detect(os.Getenv)
	}
	return protocol
}

// OpenExternal opens files in the OS default viewer (macOS: open, Linux: xdg-open).
func OpenExternal(paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", paths...).Start()
	case "linux":
		for _, p := range paths {
			if err := exec.Command("xdg-open", p).Start(); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("unsupported OS: " + runtime.GOOS)
}

// Box is the area to draw into, in terminal cells, plus the cell size in pixels
// (0 if unknown).
type Box struct {
	Cols, Rows   int
	CellW, CellH int
}

// Fit returns the size in cells that shows a w×h-pixel image as large as possible inside
// the box without distortion and without upscaling past its natural size.
func Fit(w, h int, b Box) (cols, rows int) {
	cw, ch := b.CellW, b.CellH
	if cw <= 0 || ch <= 0 {
		cw, ch = 10, 20 // typical cell aspect ratio 1:2
	}
	if w <= 0 || h <= 0 {
		return b.Cols, b.Rows
	}
	scale := math.Min(float64(b.Cols*cw)/float64(w), float64(b.Rows*ch)/float64(h))
	scale = math.Min(scale, 1)
	cols = max(1, int(math.Ceil(float64(w)*scale/float64(cw))))
	rows = max(1, int(math.Ceil(float64(h)*scale/float64(ch))))
	return min(cols, b.Cols), min(rows, b.Rows)
}

// Write draws the image at the cursor position using the given protocol.
func Write(out io.Writer, data []byte, protocol string, b Box) error {
	switch protocol {
	case ProtocolITerm:
		return writeITerm(out, data, b)
	case ProtocolKitty:
		return writeKitty(out, data, b)
	}
	return fmt.Errorf("protocol %q cannot draw inline", protocol)
}

func writeITerm(out io.Writer, data []byte, b Box) error {
	cols, rows := b.Cols, b.Rows
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		cols, rows = Fit(cfg.Width, cfg.Height, b)
	}
	_, err := fmt.Fprintf(out, "\x1b]1337;File=inline=1;size=%d;width=%d;height=%d;preserveAspectRatio=1:%s\a",
		len(data), cols, rows, base64.StdEncoding.EncodeToString(data))
	return err
}

func writeKitty(out io.Writer, data []byte, b Box) error {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("この画像形式は表示できません: %w", err)
	}
	if format != "png" {
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return err
		}
		data = buf.Bytes()
	}
	cols, rows := Fit(cfg.Width, cfg.Height, b)
	enc := base64.StdEncoding.EncodeToString(data)
	const chunk = 4096
	for i := 0; i < len(enc); i += chunk {
		end := min(i+chunk, len(enc))
		more := 0
		if end < len(enc) {
			more = 1
		}
		var err error
		if i == 0 {
			_, err = fmt.Fprintf(out, "\x1b_Ga=T,f=100,q=2,c=%d,r=%d,m=%d;%s\x1b\\", cols, rows, more, enc[i:end])
		} else {
			_, err = fmt.Fprintf(out, "\x1b_Gm=%d;%s\x1b\\", more, enc[i:end])
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Clear removes inline images drawn with the protocol (kitty keeps them until deleted).
func Clear(out io.Writer, protocol string) {
	if protocol == ProtocolKitty {
		fmt.Fprint(out, "\x1b_Ga=d,d=A,q=2\x1b\\")
	}
}
