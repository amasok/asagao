package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testguardPath is the import path of the package that keeps the tests away from the
// user's own folders and keychain (Issue #53).
const testguardPath = "github.com/amasok/asagao/internal/testguard"

// TestEveryPackageRunsItsTestsGuarded checks that every package of the module with tests
// has a TestMain that runs them through testguard (testguard.Main or testguard.Run), and
// does not run them itself: a new package that forgets it would run its tests with the
// user's HOME and XDG folders, as when a test wrote over the user's store.db (Issue #53).
func TestEveryPackageRunsItsTestsGuarded(t *testing.T) {
	g := loadModule(t)
	var bad []string
	fset := token.NewFileSet()
	for _, p := range g.packages() {
		info := g.pkgs[p]
		if len(info.tests) == 0 {
			continue
		}
		var files []*ast.File
		for _, name := range info.tests {
			f, err := parser.ParseFile(fset, filepath.Join(info.dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			files = append(files, f)
		}
		if why := guardedTestMain(files, p == "internal/testguard"); why != "" {
			bad = append(bad, p+": "+why)
		}
	}
	report(t, bad, "packages whose tests are not run through testguard (add `func TestMain(m *testing.M) { testguard.Main(m) }` in testmain_test.go; Issue #53):")
}

// TestKeyringOnlyWithTheMock checks that the tests which use go-keyring directly are in a
// test binary with internal/auth/tokenstore in it: its init puts go-keyring's in-memory mock
// in place, so the tests never reach the user's keychain (Issue #53). Elsewhere a test
// would call the real keychain.
func TestKeyringOnlyWithTheMock(t *testing.T) {
	g := loadModule(t)
	const tokenstore = "internal/auth/tokenstore"
	var bad []string
	fset := token.NewFileSet()
	for _, p := range g.packages() {
		info := g.pkgs[p]
		uses, links := false, p == tokenstore || reaches(g, p, tokenstore)
		for _, name := range info.tests {
			f, err := parser.ParseFile(fset, filepath.Join(info.dir, name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range f.Imports {
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
// testguard, or "" if they do: one TestMain(m *testing.M) calls testguard.Main(m) or
// testguard.Run(m) (whatever name the file gives the import; in package testguard itself,
// Main or Run), and nothing in it calls m.Run itself.
func guardedTestMain(files []*ast.File, self bool) string {
	for _, f := range files {
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
				return "TestMain does not take one *testing.M"
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
				return "TestMain calls " + m + ".Run itself (call testguard.Run(" + m + ") instead)"
			case !guarded:
				return "TestMain does not call testguard.Main or testguard.Run"
			}
			return ""
		}
	}
	return "no TestMain"
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
			why := guardedTestMain(files, tc.self)
			if (why == "") != tc.ok {
				t.Errorf("guardedTestMain = %q, want ok=%v", why, tc.ok)
			}
		})
	}
}
