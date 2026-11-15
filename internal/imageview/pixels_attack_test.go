package imageview

import (
	"encoding/binary"
	"io"
	"testing"
)

// The iTerm2 path sends files Go cannot read as they are, so the pixel check is skipped
// for them. A huge PNG whose IHDR checksum is wrong (Go refuses to read it, other
// decoders may not check it) or a BMP header claiming 60000×60000 then reaches the terminal.
func TestITermRefusesHugeImagesGoCannotRead(t *testing.T) {
	badCRC := append([]byte(nil), hugeHeaders(t, 60000, 60000)["png"]...)
	badCRC[29] ^= 0xff

	bmp := make([]byte, 54)
	copy(bmp, "BM")
	binary.LittleEndian.PutUint32(bmp[2:], 54)
	binary.LittleEndian.PutUint32(bmp[10:], 54)
	binary.LittleEndian.PutUint32(bmp[14:], 40)
	binary.LittleEndian.PutUint32(bmp[18:], 60000) // width
	binary.LittleEndian.PutUint32(bmp[22:], 60000) // height
	binary.LittleEndian.PutUint16(bmp[26:], 1)
	binary.LittleEndian.PutUint16(bmp[28:], 24)

	for name, data := range map[string][]byte{"png with a bad IHDR checksum": badCRC, "bmp": bmp} {
		if err := Write(io.Discard, data, ProtocolITerm, Box{Cols: 80, Rows: 24}); err == nil {
			t.Errorf("%s claiming 60000×60000 was sent to iTerm2", name)
		}
	}
}

// Name tricks around the risky-extension list.
func TestRiskyNameTricks(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"x.Command", "photo.jpg.command", "x.command..", "x.command . ", ".command",
		"x.APP", "x.TeRmInAl", "x.webloc.", "x.desktop", "x.AppImage", "x.mobileconfig",
	} {
		if !Risky(file(t, dir, name, 0o600)) {
			t.Errorf("Risky(%q) = false", name)
		}
	}
	for _, name := range []string{"photo.command.jpg", "report.pdf", "x.app.txt"} {
		if Risky(file(t, dir, name, 0o600)) {
			t.Errorf("Risky(%q) = true", name)
		}
	}
	if !Risky(dir) {
		t.Error("a folder (e.g. an .app bundle) must be risky")
	}
	if !Risky(dir + "/missing.pdf") {
		t.Error("a missing file must be treated as risky")
	}
}
