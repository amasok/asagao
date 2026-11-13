// Package browser opens URLs in the user's web browser.
package browser

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// Open opens url in the default browser (macOS: open, Linux: xdg-open).
func Open(url string) error {
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
	return exec.Command(cmd, url).Start()
}
