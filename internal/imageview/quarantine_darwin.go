package imageview

import (
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

const quarantineAttr = "com.apple.quarantine"

// markQuarantine sets the attribute a browser puts on downloads, so Gatekeeper checks
// the file when it is opened. An attribute already there is kept (it may record that
// the user has approved the file).
func markQuarantine(path string) error {
	if _, err := unix.Getxattr(path, quarantineAttr, nil); err == nil {
		return nil
	} else if !errors.Is(err, unix.ENOATTR) {
		return fmt.Errorf("quarantine %s: %w", path, err)
	}
	// flags;time;agent;event id — 0081 is what Chrome writes for a download.
	value := fmt.Sprintf("0081;%08x;asana-tui;", time.Now().Unix())
	if err := unix.Setxattr(path, quarantineAttr, []byte(value), 0); err != nil {
		return fmt.Errorf("quarantine %s: %w", path, err)
	}
	return nil
}
