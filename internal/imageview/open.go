package imageview

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Attachments come from other people (anyone who can comment on a task), so opening one
// must not run code. On macOS a file is first marked as downloaded (the quarantine
// attribute a browser sets), so Gatekeeper checks it as it would a browser download.
// Files that would run something when opened (apps, scripts, Terminal settings, links,
// installers) are not opened at all: they are shown in Finder / the file manager, where
// the user can still open them deliberately.

// Hooks replaced in tests.
var (
	goos       = runtime.GOOS
	startCmd   = func(name string, args ...string) error { return exec.Command(name, args...).Start() }
	quarantine = markQuarantine
)

// riskyExts run code, change settings or open other locations when opened.
var riskyExts = map[string]bool{}

func init() {
	for _, e := range strings.Fields(`
		app command terminal tool workflow action service shortcut definition
		sh bash zsh csh tcsh ksh fish py pyc pl rb php js jse jxa scpt scptd applescript osascript lua tcl
		jar class jnlp
		webloc inetloc fileloc afploc ftploc url desktop lnk webarchive alfredworkflow
		pkg mpkg dmg iso img sparseimage sparsebundle mobileconfig configprofile
		bundle plugin prefpane saver kext dylib so qlgenerator mdimporter osax xpc wdgt widget
		appimage run bin out elf deb rpm snap flatpakref
		exe msi msp com bat cmd ps1 psm1 vbs vbe wsf wsh hta scr cpl reg inf gadget msc
	`) {
		riskyExts["."+e] = true
	}
}

// Risky reports whether opening path could run code: a risky extension (ignoring case
// and trailing dots or spaces), or an executable file, which macOS opens in Terminal.
func Risky(path string) bool {
	ext := strings.ToLower(strings.TrimRight(filepath.Ext(strings.TrimRight(path, ". ")), ". "))
	if riskyExts[ext] {
		return true
	}
	info, err := os.Stat(path)
	return err != nil || info.IsDir() || info.Mode()&0o111 != 0
}

// OpenExternal opens files in the OS default viewer (macOS: open, Linux: xdg-open).
// Risky files are shown in the file manager instead (see OpenFiles).
func OpenExternal(paths ...string) error {
	_, err := OpenFiles(paths...)
	return err
}

// OpenFiles opens files in the OS default app after marking them as downloaded. Risky
// files are not opened but shown in Finder (macOS) or their folder is opened (Linux);
// revealed lists them so the caller can say so.
func OpenFiles(paths ...string) (revealed []string, err error) {
	var open []string
	for _, p := range paths {
		if err := quarantine(p); err != nil {
			return nil, err
		}
		if Risky(p) {
			revealed = append(revealed, p)
		} else {
			open = append(open, p)
		}
	}
	switch goos {
	case "darwin":
		if len(open) > 0 {
			if err := startCmd("open", open...); err != nil {
				return nil, err
			}
		}
		if len(revealed) > 0 {
			if err := startCmd("open", append([]string{"-R"}, revealed...)...); err != nil {
				return nil, err
			}
		}
		return revealed, nil
	case "linux":
		for _, p := range open {
			if err := startCmd("xdg-open", p); err != nil {
				return nil, err
			}
		}
		dirs := map[string]bool{}
		for _, p := range revealed {
			if d := filepath.Dir(p); !dirs[d] {
				dirs[d] = true
				if err := startCmd("xdg-open", d); err != nil {
					return nil, err
				}
			}
		}
		return revealed, nil
	}
	return nil, errors.New("unsupported OS: " + goos)
}
