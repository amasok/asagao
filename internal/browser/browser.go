// Package browser opens URLs in the user's web browser.
package browser

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"runtime"
)

// Open opens url in the default browser (macOS: open, Linux: xdg-open). Only https URLs
// are opened: `open` would also run file:// paths and custom schemes (other apps).
func Open(rawURL string) error {
	if u, err := url.Parse(rawURL); err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("https 以外の URL は開きません: %q", rawURL)
	}
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("ブラウザを開ける環境ではありません（DISPLAY が未設定）")
		}
		cmd = "xdg-open"
	default:
		return errors.New("unsupported OS: " + runtime.GOOS)
	}
	return exec.Command(cmd, rawURL).Start()
}
