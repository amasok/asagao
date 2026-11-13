package imageview

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"TERM_PROGRAM": "iTerm.app"}, ProtocolITerm},
		{map[string]string{"TERM_PROGRAM": "WezTerm"}, ProtocolITerm},
		{map[string]string{"TERM_PROGRAM": "ghostty"}, ProtocolKitty},
		{map[string]string{"TERM": "xterm-kitty"}, ProtocolKitty},
		{map[string]string{"KITTY_WINDOW_ID": "1"}, ProtocolKitty},
		{map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, ProtocolOpen},
		{map[string]string{"TERM_PROGRAM": "iTerm.app", "TMUX": "/tmp/tmux"}, ProtocolOpen},
		{map[string]string{"GHOSTTY_RESOURCES_DIR": "/Applications/Ghostty.app/x"}, ProtocolKitty},
		// Inside a multiplexer the Ghostty variable is inherited but images would not reach it.
		{map[string]string{"TERM_PROGRAM": "herdr", "HERDR_ENV": "1", "GHOSTTY_RESOURCES_DIR": "/x"}, ProtocolOpen},
		{map[string]string{"TERM_PROGRAM": "tmux", "GHOSTTY_RESOURCES_DIR": "/x"}, ProtocolOpen},
		{map[string]string{"ZELLIJ": "0", "TERM_PROGRAM": "ghostty"}, ProtocolOpen},
		{map[string]string{}, ProtocolOpen},
	} {
		if got := Detect(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("Detect(%v) = %s, want %s", tc.env, got, tc.want)
		}
	}
}

func TestFit(t *testing.T) {
	box := Box{Cols: 100, Rows: 40, CellW: 10, CellH: 20} // 1000x800 px
	for _, tc := range []struct {
		w, h       int
		cols, rows int
	}{
		{2000, 1000, 100, 25}, // wide: limited by width
		{1000, 4000, 20, 40},  // tall: limited by height (scaled to 200x800 px)
		{200, 100, 20, 5},     // small: not upscaled
	} {
		c, r := Fit(tc.w, tc.h, box)
		if c != tc.cols || r != tc.rows {
			t.Errorf("Fit(%d,%d) = %d,%d want %d,%d", tc.w, tc.h, c, r, tc.cols, tc.rows)
		}
	}
	// Unknown cell size falls back to 1:2 cells.
	if c, r := Fit(400, 400, Box{Cols: 80, Rows: 24}); c != 40 || r != 20 {
		t.Errorf("default cell fit = %d,%d", c, r)
	}
}

func testImage(t *testing.T, format string, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	var err error
	if format == "png" {
		err = png.Encode(&buf, img)
	} else {
		err = jpeg.Encode(&buf, img, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestWriteITerm(t *testing.T) {
	data := testImage(t, "jpeg", 2000, 1000)
	var out bytes.Buffer
	if err := Write(&out, data, ProtocolITerm, Box{Cols: 100, Rows: 40, CellW: 10, CellH: 20}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.HasPrefix(s, "\x1b]1337;File=inline=1;") || !strings.HasSuffix(s, "\a") {
		t.Fatalf("not an iTerm2 sequence: %q", s[:40])
	}
	if !strings.Contains(s, "width=100;height=25;preserveAspectRatio=1:") {
		t.Fatalf("size params: %q", s[:90])
	}
	payload := s[strings.Index(s, ":")+1 : len(s)-1]
	if got, _ := base64.StdEncoding.DecodeString(payload); !bytes.Equal(got, data) {
		t.Fatal("payload should be the original file")
	}
}

func TestWriteKittyConvertsAndChunks(t *testing.T) {
	data := testImage(t, "jpeg", 300, 300) // JPEG must be converted to PNG
	var out bytes.Buffer
	if err := Write(&out, data, ProtocolKitty, Box{Cols: 100, Rows: 40, CellW: 10, CellH: 20}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	chunks := strings.Split(strings.TrimSuffix(s, "\x1b\\"), "\x1b\\")
	if !strings.HasPrefix(chunks[0], "\x1b_Ga=T,f=100,q=2,c=30,r=15,") {
		t.Fatalf("first chunk header: %q", chunks[0][:40])
	}
	var enc strings.Builder
	for i, c := range chunks {
		body := c[strings.Index(c, ";")+1:]
		if len(body) > 4096 {
			t.Fatalf("chunk %d too large: %d", i, len(body))
		}
		last := i == len(chunks)-1
		if strings.Contains(c, "m=1;") == last {
			t.Fatalf("chunk %d has wrong m flag: %q", i, c[:20])
		}
		enc.WriteString(body)
	}
	raw, err := base64.StdEncoding.DecodeString(enc.String())
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.DecodeConfig(bytes.NewReader(raw)); err != nil || format != "png" {
		t.Fatalf("payload should be PNG, got %q %v", format, err)
	}
}

func TestDisplayName(t *testing.T) {
	for in, want := range map[string]string{
		"/c/1209876-receipt.png": "receipt.png",
		"/c/1209876-a-b.png":     "a-b.png",
		"/c/photo-1.png":         "photo-1.png",
	} {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
}
