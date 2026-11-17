package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// goosList are the systems whose build-tagged files are read. Their imports are merged, so
// the result is the same on every machine that runs the test (CI runs on ubuntu and macOS;
// the store has files only for windows).
var goosList = []string{"linux", "darwin", "windows"}

// TestDependencyDirection checks the imports of every package of the module against the
// tables in rules_test.go. The imports of test files are not checked: tests may use
// adapters (fake servers, a real store).
func TestDependencyDirection(t *testing.T) {
	g := loadModule(t)

	t.Run("layers", func(t *testing.T) {
		report(t, unknownPackages(g), "packages without a layer (add them to layers in rules_test.go; architecture.md, パッケージごとの置き場所):")
	})
	found := directViolations(g)
	t.Run("imports", func(t *testing.T) {
		report(t, lines(newViolations(found, allowed)), "imports that break the rules of docs/design/architecture.md (依存の決まり):")
	})
	t.Run("stale", func(t *testing.T) {
		report(t, lines(staleAllowances(found, allowed)), "allowances that no longer happen (delete them from allowed in rules_test.go, and update the table in architecture.md, 今の import と、それぞれの扱い):")
	})
	t.Run("unused external", func(t *testing.T) {
		report(t, unusedExternal(g), "permissions in external that are not used (delete them from external in rules_test.go, and update architecture.md, 使ってよい外部ライブラリ):")
	})
	t.Run("std only", func(t *testing.T) {
		report(t, indirectExternal(g, allowed), "packages that reach an external module through their imports (ADR 0004: domain, app, shared and common use the standard library only):")
	})
	t.Run("domain clock", func(t *testing.T) {
		report(t, domainClockCalls(t, g), "domain code that reads the clock or the random source (architecture.md, 依存の決まり 1: take them as arguments):")
	})
}

// TestTablesFollowTheRules checks that the tables in rules_test.go do not contradict the
// rules they implement, so that a rule cannot be loosened by editing a table alone.
func TestTablesFollowTheRules(t *testing.T) {
	var bad []string
	for mod, pkgs := range external {
		for _, p := range pkgs {
			if l, _ := layerOf(p); l != layerAdapter && l != layerEntry {
				bad = append(bad, fmt.Sprintf("external[%q] lists %s (%s): only adapters and entry points may use external modules (ADR 0004)", mod, p, l))
			}
		}
	}
	for from, tos := range adapterExceptions {
		for _, to := range append([]string{from}, tos...) {
			if l, _ := layerOf(to); l != layerAdapter {
				bad = append(bad, fmt.Sprintf("adapterExceptions lists %s (%s), which is not an adapter", to, l))
			}
		}
	}
	for _, p := range entryAdapters {
		if l, _ := layerOf(p); l != layerAdapter {
			bad = append(bad, fmt.Sprintf("entryAdapters lists %s (%s), which is not an adapter", p, l))
		}
		if slices.Contains([]string{"internal/store", "internal/asana", "internal/config", "internal/auth/tokenstore"}, p) {
			bad = append(bad, fmt.Sprintf("entryAdapters lists %s: entry points must not import it (architecture.md, 依存の決まり 4)", p))
		}
	}
	for from, tos := range entryToEntry {
		for _, to := range append([]string{from}, tos...) {
			if l, _ := layerOf(to); l != layerEntry {
				bad = append(bad, fmt.Sprintf("entryToEntry lists %s (%s), which is not an entry point", to, l))
			}
		}
	}
	seen := map[string]bool{}
	for _, v := range allowed {
		k := v.From + " → " + v.To
		if seen[k] {
			bad = append(bad, "allowed lists "+k+" twice")
		}
		seen[k] = true
		if v.Why == "" {
			bad = append(bad, "allowed lists "+k+" without the stage that removes it")
		}
		if _, ok := layerOf(v.From); !ok {
			bad = append(bad, "allowed lists "+k+", but "+v.From+" has no layer")
		}
		if isModulePackage(v.To) {
			if importRule(v.From, v.To) == "" {
				bad = append(bad, "allowed lists "+k+", which the rules already allow")
			}
		} else if slices.Contains(external[v.To], v.From) {
			bad = append(bad, "allowed lists "+k+", which external already allows")
		}
	}
	report(t, bad, "rules_test.go contradicts the rules:")
}

func report(t *testing.T, problems []string, header string) {
	t.Helper()
	if len(problems) == 0 {
		return
	}
	sort.Strings(problems)
	t.Errorf("%s\n\t%s", header, strings.Join(problems, "\n\t"))
}

func lines(vs []violation) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.From + " → " + v.To + ": " + v.Why
	}
	return out
}

// isModulePackage reports whether p names a package of this module (as the tables do:
// "internal/store", "cmd/asagao") rather than an external module.
func isModulePackage(p string) bool {
	return strings.HasPrefix(p, "internal/") || strings.HasPrefix(p, "cmd/")
}

// layerOf returns the layer of a package of the module: an exact key of layers, or else
// the longest "dir/..." key that covers it.
func layerOf(p string) (layer, bool) {
	if l, ok := layers[p]; ok {
		return l, true
	}
	best, bestLen := layer(""), -1
	for k, l := range layers {
		dir, ok := strings.CutSuffix(k, "/...")
		if ok && (p == dir || strings.HasPrefix(p, dir+"/")) && len(dir) > bestLen {
			best, bestLen = l, len(dir)
		}
	}
	return best, bestLen >= 0
}

// importRule returns why from may not import to (both packages of the module), or "" if it
// may. The rules are those of architecture.md ("依存の決まり").
func importRule(from, to string) string {
	lf, okf := layerOf(from)
	lt, okt := layerOf(to)
	if !okf || !okt {
		return "" // reported by unknownPackages
	}
	if lt == layerTest {
		return "packages with only tests are not imported"
	}
	switch lf {
	case layerWire:
		return ""
	case layerTest:
		return "packages with only tests import nothing of the module outside their tests"
	}
	if lt == layerWire {
		return "nothing imports the wiring (rule 6)"
	}
	switch lf {
	case layerDomain:
		if lt != layerDomain {
			return "domain imports only the standard library and other domain packages (rule 1)"
		}
	case layerCommon:
		return "termsafe imports only the standard library"
	case layerShared:
		if lt != layerDomain && lt != layerShared && lt != layerCommon {
			return "shared types import only domain, shared types and termsafe"
		}
	case layerApp:
		if lt == layerApp {
			return "app and sync do not import each other (rule 2, ADR 0003)"
		}
		if lt != layerDomain && lt != layerShared && lt != layerCommon {
			return "the application imports only domain, shared types and termsafe; adapters come through ports (rule 2)"
		}
	case layerAdapter:
		if lt == layerAdapter && slices.Contains(adapterExceptions[from], to) {
			return ""
		}
		if lt != layerDomain && lt != layerShared && lt != layerCommon {
			return "an adapter imports only domain, shared types, termsafe and its own external modules (rule 3)"
		}
	case layerEntry:
		switch lt {
		case layerAdapter:
			if !slices.Contains(entryAdapters, to) {
				return "entry points import only the adapters auth, richtext, imageview and browser; the others are left to the wiring (rule 4)"
			}
		case layerEntry:
			if !slices.Contains(entryToEntry[from], to) {
				return "only cli imports other entry points, tui and mcpserver (rule 5)"
			}
		}
	}
	return ""
}

// domainForbidden are standard packages that domain code does not import (rule 1): files,
// processes, the network, the database, and the random source.
var domainForbidden = []string{"os", "net", "net/http", "database/sql", "syscall", "crypto/rand", "math/rand"}

func domainForbids(imp string) bool {
	for _, p := range domainForbidden {
		if imp == p || strings.HasPrefix(imp, p+"/") {
			return true
		}
	}
	return false
}

// directViolations returns the imports of the module's packages that break a rule, allowed
// or not: imports between the module's packages, external modules used outside external,
// and standard packages that domain code does not use. An external module is reported once
// per importing package.
func directViolations(g *graph) []violation {
	var out []violation
	for _, from := range g.packages() {
		p := g.pkgs[from]
		mods := map[string]bool{}
		for _, imp := range p.imports {
			switch {
			case g.pkgs[imp] != nil:
				if why := importRule(from, imp); why != "" {
					out = append(out, violation{from, imp, why})
				}
			case g.std[imp]:
				if l, _ := layerOf(from); l == layerDomain && domainForbids(imp) {
					out = append(out, violation{from, imp, "domain does not use files, processes, the network, the database or the random source (rule 1)"})
				}
			default:
				mod := g.modules[imp]
				if !mods[mod] && !slices.Contains(external[mod], from) {
					out = append(out, violation{from, mod, "external modules are used only where external in rules_test.go lists them (rule 7, ADR 0004)"})
				}
				mods[mod] = true
			}
		}
	}
	return out
}

func edgeSet(vs []violation) map[[2]string]bool {
	set := map[[2]string]bool{}
	for _, v := range vs {
		set[[2]string{v.From, v.To}] = true
	}
	return set
}

// unusedExternal are the entries of external whose package does not import the module.
func unusedExternal(g *graph) []string {
	var out []string
	for mod, pkgs := range external {
		for _, p := range pkgs {
			info := g.pkgs[p]
			if info == nil || !slices.ContainsFunc(info.imports, func(imp string) bool { return g.modules[imp] == mod }) {
				out = append(out, fmt.Sprintf("external[%q] lists %s, which does not import it", mod, p))
			}
		}
	}
	return out
}

// newViolations are the found violations that allow does not list.
func newViolations(found, allow []violation) []violation {
	ok := edgeSet(allow)
	var out []violation
	for _, v := range found {
		if !ok[[2]string{v.From, v.To}] {
			out = append(out, v)
		}
	}
	return out
}

// staleAllowances are the lines of allow that were not found.
func staleAllowances(found, allow []violation) []violation {
	seen := edgeSet(found)
	var out []violation
	for _, v := range allow {
		if !seen[[2]string{v.From, v.To}] {
			out = append(out, v)
		}
	}
	return out
}

// unknownPackages are the module's packages that layers does not cover.
func unknownPackages(g *graph) []string {
	var out []string
	for _, p := range g.packages() {
		if _, ok := layerOf(p); !ok {
			out = append(out, p)
		}
	}
	return out
}

// stdOnly are the layers whose packages use the standard library only, even indirectly
// (ADR 0004, decision 1).
var stdOnly = []layer{layerDomain, layerApp, layerShared, layerCommon}

// indirectExternal reports, for each package in a stdOnly layer, the external modules it
// reaches through the module's packages, with the shortest chain of imports. The walk does
// not follow imports that allow lists (allowed, in TestDependencyDirection): those are already counted, and the stage that
// removes them also removes what they bring in (sync → config → go-toml). Once an allowance
// is gone, the walk follows that import too.
func indirectExternal(g *graph, allow []violation) []string {
	ok := edgeSet(allow)
	var out []string
	for _, start := range g.packages() {
		if l, _ := layerOf(start); !slices.Contains(stdOnly, l) {
			continue
		}
		prev := map[string]string{start: ""}
		queue := []string{start}
		reported := map[string]bool{}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, imp := range g.pkgs[cur].imports {
				if ok[[2]string{cur, imp}] || g.std[imp] {
					continue
				}
				if g.pkgs[imp] != nil {
					if _, seen := prev[imp]; !seen {
						prev[imp] = cur
						queue = append(queue, imp)
					}
					continue
				}
				mod := g.modules[imp]
				if cur == start || ok[[2]string{cur, mod}] || reported[mod] {
					continue // direct imports are checked by directViolations
				}
				reported[mod] = true
				chain := []string{imp}
				for p := cur; p != ""; p = prev[p] {
					chain = append(chain, p)
				}
				slices.Reverse(chain)
				out = append(out, start+" reaches "+mod+": "+strings.Join(chain, " → "))
			}
		}
	}
	return out
}

// clockFuncs are the functions of package time that read the clock or wait for it.
var clockFuncs = []string{"Now", "Since", "Until", "Sleep", "After", "AfterFunc", "Tick", "NewTicker", "NewTimer"}

// domainClockCalls parses the Go files of the domain packages and reports uses of the clock.
// (The random source is caught by domainForbidden.)
func domainClockCalls(t *testing.T, g *graph) []string {
	var out []string
	fset := token.NewFileSet()
	for _, p := range g.packages() {
		if l, _ := layerOf(p); l != layerDomain {
			continue
		}
		for _, name := range g.pkgs[p].files {
			path := filepath.Join(g.pkgs[p].dir, name)
			f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range clockUses(fset, f) {
				out = append(out, p+"/"+c)
			}
		}
	}
	return out
}

// clockUses returns "file:line: time.Now" for each use of a clockFuncs function in f,
// whatever name f gives the time package. A dot import of time is reported too, since its
// uses cannot be told apart by name.
func clockUses(fset *token.FileSet, f *ast.File) []string {
	var out []string
	names := map[string]bool{}
	for _, imp := range f.Imports {
		if path, _ := strconv.Unquote(imp.Path.Value); path != "time" {
			continue
		}
		name := "time"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			pos := fset.Position(imp.Pos())
			out = append(out, fmt.Sprintf("%s:%d: dot import of time", filepath.Base(pos.Filename), pos.Line))
		}
		names[name] = true
	}
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); ok && names[x.Name] && slices.Contains(clockFuncs, sel.Sel.Name) {
			pos := fset.Position(sel.Pos())
			out = append(out, fmt.Sprintf("%s:%d: time.%s", filepath.Base(pos.Filename), pos.Line, sel.Sel.Name))
		}
		return true
	})
	return out
}

// graph is the import graph of the module's packages, merged over goosList.
type graph struct {
	pkgs    map[string]*pkgInfo // the module's packages, by path relative to the module ("internal/store")
	std     map[string]bool     // standard packages
	modules map[string]string   // other packages: import path → module path
}

type pkgInfo struct {
	imports []string // the module's packages relative to the module; others as they are
	dir     string
	files   []string // non-test Go files, for any of goosList
}

func (g *graph) packages() []string {
	out := make([]string, 0, len(g.pkgs))
	for p := range g.pkgs {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// listedPackage is the part of `go list -json` that the checks read.
type listedPackage struct {
	ImportPath string
	Dir        string
	Standard   bool
	GoFiles    []string
	CgoFiles   []string
	Imports    []string
	Module     *struct{ Path string }
	Error      *struct{ Err string }
}

// loadModule runs `go list -deps -json ./...` at the module's root for each of goosList and
// merges the results. It skips the test if there is no go command.
func loadModule(t *testing.T) *graph {
	t.Helper()
	gocmd, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go command:", err)
	}
	cmd := exec.Command(gocmd, "list", "-m", "-json")
	cmd.Env = append(cmd.Environ(), "GOWORK=off") // with a go.work, list -m prints every module of it
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -m: %v", errOutput(err))
	}
	var mod struct{ Path, Dir string }
	if err := json.Unmarshal(out, &mod); err != nil {
		t.Fatal(err)
	}

	results := make([][]listedPackage, len(goosList))
	errs := make([]error, len(goosList))
	var wg sync.WaitGroup
	for i, goos := range goosList {
		wg.Go(func() { results[i], errs[i] = goList(gocmd, mod.Dir, goos) })
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		t.Fatal(err)
	}

	g := &graph{pkgs: map[string]*pkgInfo{}, std: map[string]bool{}, modules: map[string]string{}}
	local := func(p string) string {
		if rel, ok := strings.CutPrefix(p, mod.Path+"/"); ok {
			return rel
		}
		return p
	}
	for _, list := range results {
		for _, lp := range list {
			switch {
			case lp.Standard:
				g.std[lp.ImportPath] = true
			case lp.Module != nil && lp.Module.Path == mod.Path:
				p := g.pkgs[local(lp.ImportPath)]
				if p == nil {
					p = &pkgInfo{dir: lp.Dir}
					g.pkgs[local(lp.ImportPath)] = p
				}
				for _, imp := range lp.Imports {
					if imp != "C" && !slices.Contains(p.imports, local(imp)) {
						p.imports = append(p.imports, local(imp))
					}
				}
				for _, f := range slices.Concat(lp.GoFiles, lp.CgoFiles) {
					if !slices.Contains(p.files, f) {
						p.files = append(p.files, f)
					}
				}
			case lp.Module != nil:
				g.modules[lp.ImportPath] = lp.Module.Path
			default:
				t.Fatalf("go list: %s is neither standard nor in a module", lp.ImportPath)
			}
		}
	}
	if len(g.pkgs) == 0 {
		t.Fatal("go list found no packages of the module")
	}
	return g
}

func goList(gocmd, dir, goos string) ([]listedPackage, error) {
	cmd := exec.Command(gocmd, "list", "-deps", "-json=ImportPath,Dir,Standard,GoFiles,CgoFiles,Imports,Module,Error", "./...")
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "GOOS="+goos, "GOWORK=off")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("GOOS=%s go list: %w", goos, errOutput(err))
	}
	var list []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var lp listedPackage
		if err := dec.Decode(&lp); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("GOOS=%s go list: %w", goos, err)
		}
		if lp.Error != nil {
			return nil, fmt.Errorf("GOOS=%s go list: %s: %s", goos, lp.ImportPath, lp.Error.Err)
		}
		list = append(list, lp)
	}
	return list, nil
}

func errOutput(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(ee.Stderr))
	}
	return err
}
