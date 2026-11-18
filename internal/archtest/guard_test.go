package archtest

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// testguardPath is the import path of the package that keeps the tests away from the
// user's own folders and keychain (Issue #53).
const testguardPath = "github.com/amasok/asagao/internal/testguard"

// TestEveryPackageRunsItsTestsGuarded checks that every folder of the module with tests
// has a TestMain that runs them through testguard (testguard.Main or testguard.Run), and
// does not run them itself: a new package that forgets it would run its tests with the
// user's HOME and XDG folders, as when a test wrote over the user's store.db (Issue #53).
//
// The test files are read from the folders, not from `go list`, so tests behind a build
// tag (`//go:build realcheck`) are checked too, and the TestMain must be in a file built
// whenever any of the tests is.
func TestEveryPackageRunsItsTestsGuarded(t *testing.T) {
	g := loadModule(t)
	bad, err := unguardedDirs(g.root)
	if err != nil {
		t.Fatal(err)
	}
	report(t, bad, "packages whose tests are not run through testguard (add `func TestMain(m *testing.M) { testguard.Main(m) }` in testmain_test.go, without a build tag; Issue #53):")
}

// unguardedDirs returns, for each folder below root whose tests are not run through
// testguard, "dir: why".
func unguardedDirs(root string) ([]string, error) {
	dirs, err := testFiles(root)
	if err != nil {
		return nil, err
	}
	var bad []string
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		if why := guardedTests(dirs[dir], dir == "internal/testguard"); why != "" {
			bad = append(bad, dir+": "+why)
		}
	}
	return bad, nil
}

// testFile is one parsed test file and its build constraint.
type testFile struct {
	name       string
	f          *ast.File
	constraint string // the //go:build line and the _GOOS/_GOARCH of the name; "" for none
}

// testFiles reads every *_test.go file below root (whatever its build tags), by folder
// relative to root. Folders that the go command leaves out (testdata, vendor, names
// starting with . or _, such as .worktrees and .git) and other modules are skipped.
func testFiles(root string) (map[string][]testFile, error) {
	out := map[string][]testFile{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		out[rel] = append(out[rel], testFile{name: name, f: f, constraint: constraintOf(name, f)})
		return nil
	})
	return out, err
}

// knownOSArch are the GOOS and GOARCH values that, as the last parts of a file name
// (x_linux_test.go, x_windows_amd64_test.go), make the file built only there.
var knownOSArch = []string{
	"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js", "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos",
	"386", "amd64", "arm", "arm64", "loong64", "mips", "mipsle", "mips64", "mips64le", "ppc64", "ppc64le", "riscv64", "s390x", "wasm",
}

// constraintOf is the build constraint of a test file: its //go:build line, and the GOOS or
// GOARCH its name ends in. Files with the same constraint are built together.
func constraintOf(name string, f *ast.File) string {
	var parts []string
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if constraint.IsGoBuild(c.Text) {
				parts = append(parts, strings.TrimSpace(c.Text))
			}
		}
	}
	elems := strings.Split(strings.TrimSuffix(name, "_test.go"), "_")
	for i := len(elems) - 1; i >= 1 && i >= len(elems)-2; i-- {
		if slices.Contains(knownOSArch, elems[i]) {
			parts = append(parts, "_"+elems[i])
		}
	}
	return strings.Join(parts, " ")
}

// guardedTests returns why the test files of one folder are not all run through
// testguard, or "" if they are: a guarded TestMain (guardedTestMain), in a file built
// whenever any of the test files is (no constraint, or the one every test file has).
func guardedTests(files []testFile, self bool) string {
	asts := make([]*ast.File, len(files))
	for i, tf := range files {
		asts[i] = tf.f
	}
	why, at := guardedTestMain(asts, self)
	if why != "" {
		return why
	}
	if c := files[at].constraint; c != "" {
		for _, tf := range files {
			if tf.constraint != c {
				return fmt.Sprintf("TestMain is in %s (%s), but %s is built without it", files[at].name, c, tf.name)
			}
		}
	}
	return ""
}

// TestKeyringOnlyWithTheMock checks that the tests which use go-keyring directly are in a
// test binary with internal/auth/tokenstore in it: its init puts go-keyring's in-memory mock
// in place, so the tests never reach the user's keychain (Issue #53). Elsewhere a test
// would call the real keychain. Tests behind build tags are checked too.
func TestKeyringOnlyWithTheMock(t *testing.T) {
	g := loadModule(t)
	dirs, err := testFiles(g.root)
	if err != nil {
		t.Fatal(err)
	}
	const tokenstore = "internal/auth/tokenstore"
	var bad []string
	for _, p := range slices.Sorted(maps.Keys(dirs)) {
		uses, links := false, p == tokenstore || (g.pkgs[p] != nil && reaches(g, p, tokenstore))
		for _, tf := range dirs[p] {
			for _, imp := range tf.f.Imports {
				switch path, _ := strconv.Unquote(imp.Path.Value); {
				case path == "github.com/zalando/go-keyring" || strings.HasPrefix(path, "github.com/zalando/go-keyring/"):
					uses = true
				case path == "github.com/amasok/asagao/"+tokenstore:
					links = true
				}
			}
		}
		if uses && !links {
			bad = append(bad, p)
		}
	}
	report(t, bad, "packages whose tests use go-keyring without internal/auth/tokenstore in the test binary (its init mocks the keychain; import it, or use tokenstore's Keychain):")
}

// reaches reports whether package from imports to, directly or through other packages of
// the module (imports of non-test files).
func reaches(g *graph, from, to string) bool {
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, imp := range g.pkgs[cur].imports {
			if imp == to {
				return true
			}
			if g.pkgs[imp] != nil && !seen[imp] {
				seen[imp] = true
				queue = append(queue, imp)
			}
		}
	}
	return false
}

// guardedTestMain returns why the test files of one package do not run their tests through
// testguard, or "" and the index of its file if they do: one TestMain(m *testing.M) calls testguard.Main(m) or
// testguard.Run(m) (whatever name the file gives the import; in package testguard itself,
// Main or Run), and nothing in it calls m.Run itself.
func guardedTestMain(files []*ast.File, self bool) (string, int) {
	for i, f := range files {
		names := map[string]bool{}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == testguardPath {
				name := "testguard"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				names[name] = true
			}
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			params := fn.Type.Params.List
			if len(params) != 1 || len(params[0].Names) != 1 {
				return "TestMain does not take one *testing.M", i
			}
			m := params[0].Names[0].Name
			guarded, direct := false, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					x, ok := fun.X.(*ast.Ident)
					if !ok {
						return true
					}
					if names[x.Name] && (fun.Sel.Name == "Main" || fun.Sel.Name == "Run") {
						guarded = true
					}
					if x.Name == m && fun.Sel.Name == "Run" {
						direct = true
					}
				case *ast.Ident:
					if self && (fun.Name == "Main" || fun.Name == "Run") {
						guarded = true
					}
				}
				return true
			})
			switch {
			case direct:
				return "TestMain calls " + m + ".Run itself (call testguard.Run(" + m + ") instead)", i
			case !guarded:
				return "TestMain does not call testguard.Main or testguard.Run", i
			}
			return "", i
		}
	}
	return "no TestMain", -1
}

func TestGuardedTestMain(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  []string
		self bool
		ok   bool
	}{
		{"guarded", []string{`package p
import ("testing"; "github.com/amasok/asagao/internal/testguard")
func TestMain(m *testing.M) { testguard.Main(m) }`}, false, true},
		{"run with more", []string{`package p
import ("os"; "testing"; "github.com/amasok/asagao/internal/testguard")
func TestMain(m *testing.M) { setup(); code := testguard.Run(m); os.Exit(code) }`}, false, true},
		{"renamed import", []string{`package p
import ("testing"; tg "github.com/amasok/asagao/internal/testguard")
func TestMain(m *testing.M) { tg.Main(m) }`}, false, true},
		{"in another file", []string{`package p
func TestX(t *testing.T) {}`, `package p
import ("testing"; "github.com/amasok/asagao/internal/testguard")
func TestMain(m *testing.M) { testguard.Main(m) }`}, false, true},
		{"testguard itself", []string{`package testguard
import "testing"
func TestMain(m *testing.M) { Main(m) }`}, true, true},

		{"no TestMain", []string{`package p
import "testing"
func TestX(t *testing.T) {}`}, false, false},
		{"plain m.Run", []string{`package p
import ("os"; "testing")
func TestMain(m *testing.M) { os.Exit(m.Run()) }`}, false, false},
		{"m.Run besides testguard", []string{`package p
import ("os"; "testing"; "github.com/amasok/asagao/internal/testguard")
func TestMain(m *testing.M) { if fast { os.Exit(m.Run()) }; testguard.Main(m) }`}, false, false},
		{"another package named testguard", []string{`package p
import ("testing"; "example.com/testguard")
func TestMain(m *testing.M) { testguard.Main(m) }`}, false, false},
		{"import in another file only", []string{`package p
import "github.com/amasok/asagao/internal/testguard"
var _ = testguard.Dir`, `package p
import "testing"
func TestMain(m *testing.M) { testguard.Main(m) }`}, false, false},
		{"Main outside testguard", []string{`package p
import "testing"
func TestMain(m *testing.M) { Main(m) }`}, false, false},
		{"method named TestMain", []string{`package p
import ("testing"; "github.com/amasok/asagao/internal/testguard")
func (T) TestMain(m *testing.M) { testguard.Main(m) }`}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			var files []*ast.File
			for i, src := range tc.src {
				f, err := parser.ParseFile(fset, "x"+strconv.Itoa(i)+"_test.go", src, parser.SkipObjectResolution)
				if err != nil {
					t.Fatal(err)
				}
				files = append(files, f)
			}
			why, _ := guardedTestMain(files, tc.self)
			if (why == "") != tc.ok {
				t.Errorf("guardedTestMain = %q, want ok=%v", why, tc.ok)
			}
		})
	}
}

// The check reads the folders themselves: tests behind a build tag, or only for one
// system, are checked too, and a TestMain behind a tag does not cover the tests without it.
func TestUnguardedDirsSeesTaggedTests(t *testing.T) {
	const guarded = "package p\nimport (\"testing\"; \"github.com/amasok/asagao/internal/testguard\")\nfunc TestMain(m *testing.M) { testguard.Main(m) }\n"
	const test = "package p\nimport \"testing\"\nfunc TestX(t *testing.T) {}\n"
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/m\n",
		// Only tests behind a tag, no TestMain: go list (without the tag) sees no tests.
		"tagonly/a.go":           "package p\n",
		"tagonly/real_test.go":   "//go:build realcheck\n\n" + test,
		"tagonly/record_test.go": "//go:build realcheck\n\npackage p\n",
		// Tagged tests with the TestMain in a file without the tag: fine.
		"tagok/testmain_test.go": guarded,
		"tagok/real_test.go":     "//go:build realcheck\n\n" + test,
		// The TestMain only behind the tag: the tests without it run unguarded.
		"tagmain/testmain_test.go": "//go:build realcheck\n\n" + guarded,
		"tagmain/x_test.go":        test,
		// All tests behind the same tag as the TestMain: fine.
		"tagall/testmain_test.go": "//go:build realcheck\n\n" + guarded,
		"tagall/x_test.go":        "//go:build realcheck\n\n" + test,
		// Tests for one system only, no TestMain.
		"oneos/x_linux_test.go": test,
		// The TestMain only for one system.
		"osmain/testmain_windows_test.go": guarded,
		"osmain/x_test.go":                test,
		// Folders the go command leaves out, and another module.
		".worktrees/b/internal/x/x_test.go": test,
		"testdata/x_test.go":                test,
		"_old/x_test.go":                    test,
		"sub/go.mod":                        "module example.com/sub\n",
		"sub/x_test.go":                     test,
		// No tests: nothing to check.
		"notests/a.go": "package p\n",
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bad, err := unguardedDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, b := range bad {
		dir, _, _ := strings.Cut(b, ":")
		dirs = append(dirs, dir)
	}
	want := []string{"oneos", "osmain", "tagmain", "tagonly"}
	if !slices.Equal(dirs, want) {
		t.Errorf("unguardedDirs = %q, want the folders %q", bad, want)
	}
}

func TestConstraintOf(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"x_test.go", "package p", ""},
		{"x_test.go", "//go:build realcheck\n\npackage p", "//go:build realcheck"},
		{"x_test.go", "// Copyright\n\n//go:build !windows\n\npackage p", "//go:build !windows"},
		{"x_test.go", "package p\n\n//go:build late", ""},
		{"x_linux_test.go", "package p", "_linux"},
		{"x_windows_amd64_test.go", "package p", "_amd64 _windows"},
		{"linux_test.go", "package p", ""},
		{"x_unix_test.go", "package p", ""},
	} {
		f, err := parser.ParseFile(token.NewFileSet(), tc.name, tc.src, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		if got := constraintOf(tc.name, f); got != tc.want {
			t.Errorf("constraintOf(%s, %q) = %q, want %q", tc.name, tc.src, got, tc.want)
		}
	}
}
