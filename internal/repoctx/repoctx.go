// Package repoctx reads .asana.json, the file that ties a folder (usually a repository) to
// the Asana project, section and task worked on there. The CLI uses it as the default
// project of `task create` and `task list` and the default task of `task show`; the MCP
// server returns it from get_context; both print it with `asagao context`.
//
//	{"project": "新居", "section": "家具", "task": "1203455468597469"}
//
// Every field is optional. project and section are a gid or a name (as --project and
// --section take them); task is a task gid.
package repoctx

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// FileName is the name of the file.
const FileName = ".asana.json"

// maxSize is the most of the file that is read: it holds three short strings.
const maxSize = 64 << 10

// Context is the content of a .asana.json.
type Context struct {
	Project string `json:"project,omitempty"`
	Section string `json:"section,omitempty"`
	Task    string `json:"task,omitempty"`
	// Path is the file it was read from.
	Path string `json:"-"`
}

// Find looks for .asana.json in dir, then in each parent folder up to the root of the
// repository dir is in (the first folder that has a .git), the home folder, or the root of
// the file system, whichever comes first. It returns nil (and no error) when there is none.
// A file that is there but cannot be read or is invalid is an error: silently working on
// another project than the one written down would be worse.
func Find(dir string) (*Context, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	for {
		path := filepath.Join(dir, FileName)
		if _, err := os.Lstat(path); err == nil {
			return Load(path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return nil, nil // the root of the repository: do not look outside it
		}
		if home != "" && dir == filepath.Clean(home) {
			return nil, nil // not above the home folder
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		dir = parent
	}
}

// Load reads the .asana.json at path.
func Load(path string) (*Context, error) {
	fail := func(err error) (*Context, error) { return nil, fmt.Errorf("%s: %w", path, err) }
	fi, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	if !fi.Mode().IsRegular() { // a FIFO would block the read
		return fail(errors.New("通常のファイルではありません"))
	}
	// Someone else's file (in a shared folder such as /tmp) must not choose the project.
	if !ownedByMe(fi) {
		return fail(errors.New("ほかのユーザーのファイルです"))
	}
	f, err := os.Open(path)
	if err != nil {
		return fail(err)
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return fail(err)
	}
	if len(b) > maxSize {
		return fail(fmt.Errorf("大きすぎます（%d KB まで）", maxSize>>10))
	}
	c, err := Parse(b)
	if err != nil {
		return fail(err)
	}
	c.Path = path
	return c, nil
}

// Parse reads the content of a .asana.json. Unknown keys are an error (a misspelt key
// would otherwise be ignored without a word).
func Parse(b []byte) (*Context, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Context
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("JSON として読めません: %w", err)
	}
	if dec.More() {
		return nil, errors.New("JSON のあとに余計なものがあります")
	}
	c.Project = strings.TrimSpace(c.Project)
	c.Section = strings.TrimSpace(c.Section)
	c.Task = strings.TrimSpace(c.Task)
	if c.Task != "" && strings.Trim(c.Task, "0123456789") != "" {
		// it goes into Asana's URL (/tasks/<gid>): a gid is digits only
		return nil, errors.New(`"task" はタスクの gid（数字）で指定してください`)
	}
	if c.Section != "" && c.Project == "" {
		return nil, errors.New(`"section" は "project" と一緒に指定してください`)
	}
	for k, v := range map[string]string{"project": c.Project, "section": c.Section, "task": c.Task} {
		if strings.ContainsFunc(v, isControl) {
			return nil, fmt.Errorf("%q に制御文字があります", k)
		}
	}
	return &c, nil
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }
