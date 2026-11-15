package imageview

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hugeHeaders returns small files whose headers claim w×h pixels.
func hugeHeaders(t *testing.T, w, h int) map[string][]byte {
	t.Helper()
	small := image.NewRGBA(image.Rect(0, 0, 1, 1))
	small.Set(0, 0, color.White)
	out := map[string][]byte{}

	var b bytes.Buffer
	_ = png.Encode(&b, small)
	p := b.Bytes() // signature (8) | length (4) "IHDR" (4) width (4) height (4) ... crc
	binary.BigEndian.PutUint32(p[16:], uint32(w))
	binary.BigEndian.PutUint32(p[20:], uint32(h))
	binary.BigEndian.PutUint32(p[29:], crc32.ChecksumIEEE(p[12:29]))
	out["png"] = p

	b = bytes.Buffer{}
	_ = gif.Encode(&b, small, nil)
	g := b.Bytes() // "GIF89a" | width (2, LE) | height (2, LE)
	binary.LittleEndian.PutUint16(g[6:], uint16(w))
	binary.LittleEndian.PutUint16(g[8:], uint16(h))
	out["gif"] = g

	b = bytes.Buffer{}
	_ = jpeg.Encode(&b, small, nil)
	j := b.Bytes()
	i := bytes.Index(j, []byte{0xff, 0xc0}) // SOF0: marker | length (2) | precision (1) | height (2) | width (2)
	if i < 0 {
		t.Fatal("no SOF0 in the test JPEG")
	}
	binary.BigEndian.PutUint16(j[i+5:], uint16(h))
	binary.BigEndian.PutUint16(j[i+7:], uint16(w))
	out["jpeg"] = j
	return out
}

// A few-KB file claiming 60000×60000 pixels is refused before it is decoded.
func TestHugeImagesAreRefused(t *testing.T) {
	for format, data := range hugeHeaders(t, 60000, 60000) {
		if cfg, f, err := image.DecodeConfig(bytes.NewReader(data)); err != nil || f != format || cfg.Width != 60000 {
			t.Fatalf("%s: test file header = %+v %s %v", format, cfg, f, err)
		}
		if _, _, _, err := ToPNG(data); err == nil || !strings.Contains(err.Error(), "大きすぎます") {
			t.Errorf("%s: ToPNG err = %v", format, err)
		}
		for _, protocol := range []string{ProtocolKitty, ProtocolITerm} {
			if err := Write(io.Discard, data, protocol, Box{Cols: 80, Rows: 24}); err == nil || !strings.Contains(err.Error(), "大きすぎます") {
				t.Errorf("%s via %s: Write err = %v", format, protocol, err)
			}
		}
	}
}

func TestPixelLimit(t *testing.T) {
	for _, tc := range []struct {
		w, h int
		ok   bool
	}{
		{8064, 6048, true}, // 48MP phone photo
		{MaxPixels, 1, true},
		{MaxPixels + 1, 1, false},
		{10000, 5001, false},
		{0, 10, false},
	} {
		err := checkPixels(image.Config{Width: tc.w, Height: tc.h})
		if (err == nil) != tc.ok {
			t.Errorf("%d×%d: err = %v", tc.w, tc.h, err)
		}
	}
}

// An image whose size cannot be checked (Go cannot read it: HEIC, BMP, a damaged PNG)
// is not sent to the terminal; the viewer opens it in the OS viewer, once.
func TestUnknownSizeOpensExternally(t *testing.T) {
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic\x00\x00\x00\x00mif1heic")...)
	for _, protocol := range []string{ProtocolITerm, ProtocolKitty} {
		var out strings.Builder
		if err := Write(&out, heic, protocol, Box{Cols: 80, Rows: 24}); !errors.Is(err, ErrUnknownSize) || out.Len() != 0 {
			t.Errorf("%s: err = %v, wrote %q", protocol, err, out.String())
		}
	}
	path := filepath.Join(t.TempDir(), "123-IMG_0001.HEIC")
	if err := os.WriteFile(path, heic, 0o600); err != nil {
		t.Fatal(err)
	}
	var opened []int
	external := func(i int) error { opened = append(opened, i); return nil }
	var out strings.Builder
	draw(&out, []string{path}, 0, ProtocolITerm, Box{Cols: 80, Rows: 24}, external)
	if len(opened) != 1 || strings.Contains(out.String(), "1337;File=") || !strings.Contains(out.String(), "外部ビューアで開きました") {
		t.Errorf("opened %v, out %q", opened, out.String())
	}
}
