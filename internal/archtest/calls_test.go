package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Rules that imports alone cannot check (dependency-rules.md, 関数の決まり): an entry point
// may import richtext and dates, but converting Markdown to Asana rich text and reading a
// due date are the use cases' (internal/app). The check reads the entry points' files with
// go/parser and looks for these names; it does not resolve types.

const modulePath = "github.com/amasok/asagao/"

// entryForbiddenCalls are, for each package, the selectors an entry point may not use:
// "Parse" is dates.Parse, "TaskNotes.FromMarkdown" is richtext.TaskNotes.FromMarkdown.
var entryForbiddenCalls = map[string][]string{
	"internal/dates": {"Parse"},
	"internal/richtext": {
		"TaskNotes.FromMarkdown", "TaskNotes.PlainFromMarkdown",
		"Comment.FromMarkdown", "Comment.PlainFromMarkdown",
		"StatusUpdate.FromMarkdown", "StatusUpdate.PlainFromMarkdown",
		// the conversion the use cases write with: internal/wire gives it to them (#71)
		"Converter",
	},
}

// entryForbiddenLiterals are, for each package, the types an entry point may not make a
// value of itself (#71): the use cases and the query, which it gets from app.UseCases
// (internal/wire puts them together over the store), and the sync engine and its parts,
// which it gets from the wiring too. Tests are not checked: they may build any of them.
var entryForbiddenLiterals = map[string][]string{
	"internal/app": {
		"Tasks", "Comments", "Status", "Briefs", "Projects", "Relations", "Sections", "TaskOps",
		"Tags", "Copies", "FieldOptions", "ProjectFields", "Notifications", "Outbox", "Goals",
		"TimeTracking", "PushNow", "GitLink", "QuickAdd", "Query", "UseCases", "Ports",
	},
	"internal/sync": {"Syncer", "Pusher", "Puller", "Details", "Goals", "Templates", "Search", "Projects"},
}

// storeWrites says whether a method name is one of the store's local writes, which an
// entry point leaves to the use cases: the names that end in "Local" (CreateTaskLocal, …;
// only the store has them) and ApplyTagEdit. Only method calls are checked: x.Name where x
// is not an imported package, so a package's constant such as app.KeepLocal is not one.
// A name the file also binds to something else (a parameter "sync *store.Store") is not
// taken for the package anywhere in the file (see rebound).
func storeWrites(name string) bool {
	return name == "ApplyTagEdit" || (strings.HasSuffix(name, "Local") && name != "Local" && ast.IsExported(name))
}

// allowedCalls are the uses that exist today, as "internal/tui/edit.go: richtext.TaskNotes.FromMarkdown"
// or "internal/tui/board.go: .MoveTaskLocal" (a store write). Stage a5 removed the last
// conversion and stage a6 the last store write; a stage that adds a rule lists its uses
// here and removes them as it goes.
var allowedCalls = []string{}

func TestEntryPointsLeaveConversionsToApp(t *testing.T) {
	g := loadModule(t)
	found := entryCalls(t, g)
	var news, stale []string
	for _, c := range found {
		if !slices.Contains(allowedCalls, key(c)) {
			news = append(news, c)
		}
	}
	for _, a := range allowedCalls {
		if !slices.ContainsFunc(found, func(c string) bool { return key(c) == a }) {
			stale = append(stale, a)
		}
	}
	t.Run("calls", func(t *testing.T) {
		report(t, news, "entry points that convert Markdown, read a due date or call a store write themselves (dependency-rules.md, 関数の決まり: call the use case in internal/app):")
	})
	t.Run("stale", func(t *testing.T) {
		report(t, stale, "allowedCalls that no longer happen (delete them from allowedCalls in calls_test.go):")
	})
}

// key drops the line number: "internal/tui/edit.go:12: x" → "internal/tui/edit.go: x".
func key(c string) string {
	file, rest, _ := strings.Cut(c, ":")
	_, name, _ := strings.Cut(rest, ": ")
	return file + ": " + name
}

func entryCalls(t *testing.T, g *graph) []string {
	var out []string
	fset := token.NewFileSet()
	for _, p := range g.packages() {
		if l, _ := layerOf(p); l != layerEntry {
			continue
		}
		for _, name := range g.pkgs[p].files {
			f, err := parser.ParseFile(fset, filepath.Join(g.pkgs[p].dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range forbiddenCalls(fset, f) {
				out = append(out, p+"/"+c)
			}
			for _, c := range forbiddenLiterals(fset, f) {
				out = append(out, p+"/"+c)
			}
		}
	}
	return out
}

// forbiddenCalls returns "file:line: dates.Parse" for each use of an entryForbiddenCalls
// selector in f, whatever name f gives the package, and "file:line: .MoveTaskLocal" for
// each use of a store write (storeWrites) as a method. A dot import of one of the packages
// is reported too, since its uses cannot be told apart by name.
func forbiddenCalls(fset *token.FileSet, f *ast.File) []string {
	var out []string
	pkgOf := map[string]string{}  // the name in f → "internal/dates"
	imported := map[string]bool{} // every package name in f
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		if imp.Name != nil {
			imported[imp.Name.Name] = true
		} else {
			imported[filepath.Base(path)] = true
		}
		rel, ok := strings.CutPrefix(path, modulePath)
		if _, banned := entryForbiddenCalls[rel]; !ok || !banned {
			continue
		}
		name := filepath.Base(rel)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." {
			pos := fset.Position(imp.Pos())
			out = append(out, fmt.Sprintf("%s:%d: dot import of %s", filepath.Base(pos.Filename), pos.Line, rel))
		}
		pkgOf[name] = rel
	}
	shadowed := rebound(f)
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if storeWrites(sel.Sel.Name) {
			if id, ok := sel.X.(*ast.Ident); !ok || !imported[id.Name] || shadowed[id.Name] {
				pos := fset.Position(sel.Sel.Pos())
				out = append(out, fmt.Sprintf("%s:%d: .%s", filepath.Base(pos.Filename), pos.Line, sel.Sel.Name))
				return true
			}
		}
		// sel is pkg.Name or pkg.Target.Name
		name, x := sel.Sel.Name, sel.X
		if inner, ok := x.(*ast.SelectorExpr); ok {
			name, x = inner.Sel.Name+"."+name, inner.X
		}
		id, ok := x.(*ast.Ident)
		if !ok {
			return true
		}
		if rel, ok := pkgOf[id.Name]; ok && slices.Contains(entryForbiddenCalls[rel], name) {
			pos := fset.Position(sel.Pos())
			out = append(out, fmt.Sprintf("%s:%d: %s.%s", filepath.Base(pos.Filename), pos.Line, filepath.Base(rel), name))
		}
		return true
	})
	return out
}

// forbiddenLiterals returns "file:line: app.Tasks{}" for each composite literal in f of an
// entryForbiddenLiterals type (&app.Tasks{...} and app.Tasks{...} alike), whatever name f
// gives the package.
func forbiddenLiterals(fset *token.FileSet, f *ast.File) []string {
	pkgOf := map[string]string{} // the name in f → "internal/app"
	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		rel, ok := strings.CutPrefix(path, modulePath)
		if _, banned := entryForbiddenLiterals[rel]; !ok || !banned {
			continue
		}
		name := filepath.Base(rel)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		pkgOf[name] = rel
	}
	shadowed := rebound(f)
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || shadowed[id.Name] {
			return true
		}
		if rel, ok := pkgOf[id.Name]; ok && slices.Contains(entryForbiddenLiterals[rel], sel.Sel.Name) {
			pos := fset.Position(lit.Pos())
			out = append(out, fmt.Sprintf("%s:%d: %s.%s{}", filepath.Base(pos.Filename), pos.Line, filepath.Base(rel), sel.Sel.Name))
		}
		return true
	})
	return out
}

// rebound returns the names that f declares as something other than an import: functions,
// types, variables and constants, parameters, results and receivers, and the names that
// := and range define. It does not follow scopes (the parser's object resolution may be
// off): a name rebound anywhere in the file is not taken for the package in the whole file,
// so a store write on it is never missed, at the cost of reporting a package's own
// Local-named identifier in such a file (rename the variable then).
func rebound(f *ast.File) map[string]bool {
	names := map[string]bool{}
	add := func(ids ...*ast.Ident) {
		for _, id := range ids {
			if id != nil {
				names[id.Name] = true
			}
		}
	}
	fields := func(l *ast.FieldList) {
		if l == nil {
			return
		}
		for _, fld := range l.List {
			add(fld.Names...)
		}
	}
	idents := func(es ...ast.Expr) {
		for _, e := range es {
			if id, ok := e.(*ast.Ident); ok {
				add(id)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncDecl:
			add(n.Name)
			fields(n.Recv)
		case *ast.FuncType:
			fields(n.TypeParams)
			fields(n.Params)
			fields(n.Results)
		case *ast.TypeSpec:
			add(n.Name)
		case *ast.ValueSpec:
			add(n.Names...)
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				idents(n.Lhs...)
			}
		case *ast.RangeStmt:
			if n.Tok == token.DEFINE {
				idents(n.Key, n.Value)
			}
		case *ast.TypeSwitchStmt:
			if a, ok := n.Assign.(*ast.AssignStmt); ok && a.Tok == token.DEFINE {
				idents(a.Lhs...)
			}
		}
		return true
	})
	return names
}

// A store write on a variable named like an import is still a store write, wherever the
// name is rebound; the package's own names stay allowed where it is not.
func TestForbiddenCallsSeeRebindingsOfImportNames(t *testing.T) {
	for _, c := range []struct {
		src  string
		want []string
	}{
		{"func f(sync *store.Store) { _ = sync.MoveTaskLocal(ctx) }", []string{"x.go:8: .MoveTaskLocal"}},
		{"func f() { store := open(); _ = store.ApplyTagEdit(ctx) }", []string{"x.go:8: .ApplyTagEdit"}},
		{"func f() { for _, sync := range all { sync.AddTagLocal(ctx) } }", []string{"x.go:8: .AddTagLocal"}},
		{"var sync = open()\nfunc f() { sync.SetFollowerLocal(ctx) }", []string{"x.go:9: .SetFollowerLocal"}},
		{"func (sync *T) f() { sync.SetProjectLocal(ctx) }", []string{"x.go:8: .SetProjectLocal"}},
		{"func f() { switch sync := x.(type) { default: sync.MoveTaskLocal(ctx) } }", []string{"x.go:8: .MoveTaskLocal"}},
		{"func f(s *store.Store) { _ = sync.KeepLocal; _ = s.MoveTaskLocal(ctx) }", []string{"x.go:8: .MoveTaskLocal"}},
	} {
		src := "package x\n\nimport (\n\t\"github.com/amasok/asagao/internal/store\"\n\t\"github.com/amasok/asagao/internal/sync\"\n)\n\n" + c.src + "\n"
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", c.src, err)
		}
		if got := forbiddenCalls(fset, f); !slices.Equal(got, c.want) {
			t.Errorf("%s:\ngot  %v\nwant %v", c.src, got, c.want)
		}
	}
}

func TestForbiddenCallsFindsTheNames(t *testing.T) {
	src := `package x

import (
	"time"

	d "github.com/amasok/asagao/internal/dates"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/sync"
)

func f(now time.Time) {
	d.Parse("today", now)
	_ = richtext.TaskNotes.FromMarkdown("a")
	_ = richtext.Comment.PlainFromMarkdown("a")
	fn := richtext.StatusUpdate.FromMarkdown
	_ = fn
	_ = richtext.ToEditableMarkdown("a") // a display conversion: allowed
	_, _ = time.Parse(time.DateOnly, "x") // another package's Parse
	_, _ = app.ParseDue("today", now)
	_ = richtext.TaskNotes // the target alone
	_ = st.MoveTaskLocal(ctx)
	_ = s.Store.ApplyTagEdit(ctx)
	w := m.deps.Store.SetProjectLocal
	_ = sync.KeepLocal       // a package's name: not a store write
	_ = st.isLocal()         // unexported: not the store's
	_ = st.ReplaceLocalGID() // does not end in Local
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := forbiddenCalls(fset, f)
	want := []string{"x.go:13: dates.Parse", "x.go:14: richtext.TaskNotes.FromMarkdown", "x.go:15: richtext.Comment.PlainFromMarkdown", "x.go:16: richtext.StatusUpdate.FromMarkdown",
		"x.go:22: .MoveTaskLocal", "x.go:23: .ApplyTagEdit", "x.go:24: .SetProjectLocal"}
	if !slices.Equal(got, want) {
		t.Errorf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	dot := `package x

import . "github.com/amasok/asagao/internal/dates"

var _, _ = Parse("today", Now())
`
	f, err = parser.ParseFile(fset, "y.go", dot, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := forbiddenCalls(fset, f); !slices.Equal(got, []string{"y.go:3: dot import of internal/dates"}) {
		t.Errorf("dot import: %v", got)
	}
	if got := key("internal/tui/edit.go:12: dates.Parse"); got != "internal/tui/edit.go: dates.Parse" {
		t.Errorf("key = %q", got)
	}
}

// The literal check finds the use cases and syncers an entry point makes itself, under
// whatever name the file gives the package, and leaves their types and other values alone.
func TestForbiddenLiteralsFindsThem(t *testing.T) {
	src := `package x

import (
	usecase "github.com/amasok/asagao/internal/app"
	"github.com/amasok/asagao/internal/richtext"
	"github.com/amasok/asagao/internal/sync"
)

func f(st S) *usecase.Tasks {
	_ = usecase.Query{QueryStore: st}
	_ = &sync.Syncer{Store: st}
	_ = usecase.CreateTaskInput{Name: "a"} // an input: allowed
	_ = []usecase.Tasks{}                   // a slice of them: not a use case made
	_ = richtext.Converter{}
	return &usecase.Tasks{Store: st}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := append(forbiddenCalls(fset, f), forbiddenLiterals(fset, f)...)
	want := []string{"x.go:14: richtext.Converter", "x.go:10: app.Query{}", "x.go:11: sync.Syncer{}", "x.go:15: app.Tasks{}"}
	if !slices.Equal(got, want) {
		t.Errorf("found %q, want %q", got, want)
	}
}
