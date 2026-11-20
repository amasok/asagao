package sync

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// partPorts are the parts of a sync (syncer.go) and the ports each asks for (ports.go).
var partPorts = map[string][2]string{
	"Pusher":    {"PushAPI", "PushStore"},
	"Puller":    {"PullAPI", "PullStore"},
	"Details":   {"DetailsAPI", "DetailsStore"},
	"Goals":     {"GoalsAPI", "GoalsStore"},
	"Templates": {"TemplatesAPI", "TemplatesStore"},
	"Search":    {"SearchAPI", "SearchStore"},
	"Projects":  {"ProjectsAPI", "ProjectsStore"},
}

// TestPartsAskOnlyForWhatTheyUse checks that every method a part's port lists itself (not
// through a shared port it embeds, such as PlanStore) is called by that part: a port must
// not grow back into the whole API or store (#71).
func TestPartsAskOnlyForWhatTheyUse(t *testing.T) {
	files := parseSyncSources(t)
	used := map[string]map[string]bool{} // "PushAPI" -> methods called on s.API by a Pusher
	for _, f := range files {
		ast.Inspect(f, func(n ast.Node) bool {
			var typ *ast.FieldList
			var body *ast.BlockStmt
			switch fn := n.(type) {
			case *ast.FuncDecl:
				body = fn.Body
				typ = fn.Type.Params
				if fn.Recv != nil {
					typ = &ast.FieldList{List: append(append([]*ast.Field{}, fn.Recv.List...), fn.Type.Params.List...)}
				}
			case *ast.FuncLit:
				typ, body = fn.Type.Params, fn.Body
			default:
				return true
			}
			if body == nil {
				return true
			}
			for _, field := range typ.List {
				part := partOf(field.Type)
				ports, ok := partPorts[part]
				if !ok {
					continue
				}
				for _, name := range field.Names {
					collectCalls(body, name.Name, ports, used)
				}
			}
			return true
		})
	}
	declared := portMethods(files)
	for part, ports := range partPorts {
		for _, port := range ports {
			ms, ok := declared[port]
			if !ok {
				t.Errorf("%s: port %s is not declared", part, port)
				continue
			}
			if len(ms) == 0 {
				t.Errorf("%s: port %s lists no method", part, port)
			}
			var unused []string
			for _, m := range ms {
				if !used[port][m] {
					unused = append(unused, m)
				}
			}
			sort.Strings(unused)
			if len(unused) > 0 {
				t.Errorf("%s does not call %s.%s: take them out of the port", part, port, strings.Join(unused, ", "))
			}
		}
	}
}

// TestPortsCheckerSeesUnusedMethods makes sure the check above finds a method no part calls.
func TestPortsCheckerSeesUnusedMethods(t *testing.T) {
	src := `package sync
type GoalsAPI interface {
	Goal(ctx context.Context, gid string) (*model.Goal, error)
	Goals(ctx context.Context, ws string) ([]model.Goal, error)
}
func (s *Goals) f() { _, _ = s.API.Goal(nil, "") }
func g(s *Goals) { _ = func(p *Goals) { p.API.Goals(nil, "") } }
`
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	used := map[string]map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok && fd.Recv != nil {
			collectCalls(fd.Body, "s", partPorts["Goals"], used)
		}
		return true
	})
	if !used["GoalsAPI"]["Goal"] || used["GoalsAPI"]["Goals"] {
		t.Errorf("used = %v, want only Goal (Goals is called on p, not on the receiver)", used)
	}
	if got := portMethods([]*ast.File{f})["GoalsAPI"]; len(got) != 2 {
		t.Errorf("declared GoalsAPI = %v, want Goal and Goals", got)
	}
}

func parseSyncSources(t *testing.T) []*ast.File {
	t.Helper()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fs := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fs, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// partOf is the part a parameter or receiver of type expr is ("": none).
func partOf(expr ast.Expr) string {
	if st, ok := expr.(*ast.StarExpr); ok {
		expr = st.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// collectCalls records the methods called as v.API.M and v.Store.M in body.
func collectCalls(body ast.Node, v string, ports [2]string, used map[string]map[string]bool) {
	ast.Inspect(body, func(n ast.Node) bool {
		se, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		inner, ok := se.X.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if id, ok := inner.X.(*ast.Ident); !ok || id.Name != v {
			return true
		}
		port := ""
		switch inner.Sel.Name {
		case "API":
			port = ports[0]
		case "Store":
			port = ports[1]
		default:
			return true
		}
		if used[port] == nil {
			used[port] = map[string]bool{}
		}
		used[port][se.Sel.Name] = true
		return true
	})
}

// portMethods are the methods each interface type lists itself (embedded ones left out).
func portMethods(files []*ast.File) map[string][]string {
	out := map[string][]string{}
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts := spec.(*ast.TypeSpec)
				it, ok := ts.Type.(*ast.InterfaceType)
				if !ok {
					continue
				}
				ms := []string{}
				for _, m := range it.Methods.List {
					if _, isFunc := m.Type.(*ast.FuncType); isFunc {
						for _, n := range m.Names {
							ms = append(ms, n.Name)
						}
					}
				}
				out[ts.Name.Name] = ms
			}
		}
	}
	return out
}
