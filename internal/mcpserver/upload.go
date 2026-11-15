package mcpserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// UploadPolicy limits which local files upload_attachment may send to Asana. An agent can
// be steered by text it reads (a task description, a web page), so without a limit it
// could be told to attach ~/.ssh/id_ed25519 or a token file. The CLI's `task attach` is
// typed by the user and is not limited.
type UploadPolicy struct {
	// Allowed folders: a file must be inside one of them (after resolving symlinks).
	Allowed []string
	// Denied folders: never uploaded even inside an allowed folder (asana-tui's own
	// config, data and cache: the token file, the store, downloaded attachments).
	Denied []string
}

// NewUploadPolicy builds the default policy: the folder the server was started in (unless
// that is /, the home folder or a hidden folder of it, where it would allow too much),
// ~/Downloads and ~/Desktop, plus the folders in config ([mcp] upload_dirs).
func NewUploadPolicy(cwd, home string, extra, denied []string) UploadPolicy {
	var p UploadPolicy
	if cwd != "" && usableCwd(cwd, home) {
		p.Allowed = append(p.Allowed, cwd)
	}
	if home != "" {
		p.Allowed = append(p.Allowed, filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop"))
	}
	p.Allowed = append(p.Allowed, extra...)
	p.Denied = denied
	return p
}

func usableCwd(cwd, home string) bool {
	// Compare the real folders: os.Getwd returns $PWD, which may be a symlink to the
	// home folder or to ~/.ssh, and Check resolves the allowed folders.
	cwd = resolveDir(cwd)
	if cwd == string(filepath.Separator) || filepath.Dir(cwd) == cwd { // / or C:\
		return false
	}
	if home == "" {
		return true
	}
	home = resolveDir(home)
	if within(home, cwd) { // the home folder or one of its parents
		return false
	}
	if rel, ok := relInside(fold(home), fold(cwd)); ok && strings.HasPrefix(rel, ".") { // ~/.ssh, ~/.config/...
		return false
	}
	return true
}

// Check resolves path and returns the real file to upload, or why it is refused.
func (p UploadPolicy) Check(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("upload_attachment: pass an absolute path (got %q)", path)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return "", err
	}
	for _, d := range p.Denied {
		if d == "" {
			continue
		}
		if within(real, resolveDir(d)) || within(real, filepath.Clean(d)) {
			return "", errors.New("upload_attachment: files of asana-tui itself (config, token, store, cache) cannot be uploaded")
		}
	}
	for _, a := range p.Allowed {
		if a == "" {
			continue
		}
		rel, ok := relInside(resolveDir(a), real)
		if !ok {
			continue
		}
		if hidden(rel) {
			return "", errors.New("upload_attachment: hidden files and files in hidden folders (names starting with \".\") cannot be uploaded")
		}
		// A hard link has no symlink to resolve: a link here to ~/.ssh/id_ed25519 or to
		// asana-tui's token would look like an ordinary file of this folder.
		if info, err := os.Stat(real); err == nil && info.Mode().IsRegular() && links(info) > 1 {
			return "", errors.New("upload_attachment: files with more than one hard link cannot be uploaded (copy the file instead)")
		}
		return real, nil
	}
	return "", fmt.Errorf("upload_attachment: %s is not in a folder that allows uploads (%s). "+
		"Ask the user to move the file to one of them, or to add its folder to upload_dirs under [mcp] in asana-tui's config.toml",
		path, strings.Join(p.Allowed, ", "))
}

// resolveDir resolves symlinks of a folder (e.g. /var -> /private/var on macOS) if it exists.
func resolveDir(d string) string {
	if r, err := filepath.EvalSymlinks(d); err == nil {
		d = r
	}
	if a, err := filepath.Abs(d); err == nil {
		d = a
	}
	return d
}

// relInside returns path relative to dir when path is dir itself or inside it.
func relInside(dir, path string) (string, bool) {
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

// within reports whether path is dir or inside it. On macOS and Windows file names are
// usually case-insensitive, so ~/.Config/ASANA-TUI must match too.
func within(path, dir string) bool {
	_, ok := relInside(fold(dir), fold(path))
	return ok
}

// fold lowercases a path where file names are usually case-insensitive.
func fold(path string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func hidden(rel string) bool {
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if strings.HasPrefix(part, ".") && part != "." {
			return true
		}
	}
	return false
}

// uploadFolders lists the allowed folders for the tool description.
func (s *Server) uploadFolders() string {
	if len(s.Upload.Allowed) == 0 {
		return "(none configured)"
	}
	return strings.Join(s.Upload.Allowed, ", ")
}
