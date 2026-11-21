package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The store reads the payload of an outbox entry as its kind's struct (outbox.PayloadOf,
// BestEffortPayload, PayloadFields …), never key by key out of the stored form (a map),
// so that a key name or a value type mistyped there is caught by the struct, and an entry
// this version cannot read is handled as the struct says (held: as far as it reads). The
// stored form itself is still handed on as it is (Op.Payload read from the outbox column,
// a discarded entry's payload kept, len of it: whether anything is left to send). Tests are
// not checked: they look at stored forms on purpose.

// payloadReaders are the packages checked.
var payloadReaders = []string{"internal/store"}

func TestStoreReadsPayloadsThroughTheirStructs(t *testing.T) {
	g := loadModule(t)
	var found []string
	fset := token.NewFileSet()
	for _, p := range payloadReaders {
		info, ok := g.pkgs[p]
		if !ok {
			t.Fatalf("package %s not found", p)
		}
		for _, name := range info.files {
			f, err := parser.ParseFile(fset, filepath.Join(info.dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range payloadMapReads(fset, f) {
				found = append(found, p+"/"+r)
			}
		}
	}
	report(t, found, "outbox payloads read key by key from the stored form (read them as the kind's struct: outbox.PayloadOf, BestEffortPayload or PayloadFields):")
}

// payloadMapReads returns "file:line: what" for each read of an outbox payload as a map
// in f: x.Payload[k] (a read or a write), range over x.Payload, x.Payload handed to a
// function (but payloadTakers and outbox.*), and the payload column
// decoded (termsafe.Unmarshal / json.Unmarshal of data named payload) into anything
// else than a Payload field (Op.Payload, Discard.Payload: the stored form kept as it is).
// It goes by names only (no types).
func payloadMapReads(fset *token.FileSet, f *ast.File) []string {
	var out []string
	at := func(n ast.Node, what string) {
		pos := fset.Position(n.Pos())
		out = append(out, fmt.Sprintf("%s:%d: %s", filepath.Base(pos.Filename), pos.Line, what))
	}
	isPayload := func(e ast.Expr) bool {
		s, ok := ast.Unparen(e).(*ast.SelectorExpr)
		return ok && s.Sel.Name == "Payload"
	}
	namesPayload := func(e ast.Expr) bool {
		found := false
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && strings.Contains(strings.ToLower(id.Name), "payload") {
				found = true
			}
			return !found
		})
		return found
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IndexExpr:
			if isPayload(n.X) {
				at(n, "Payload[…]")
			}
		case *ast.RangeStmt:
			if isPayload(n.X) {
				at(n, "range ….Payload")
			}
		case *ast.CallExpr:
			if name := callName(n); !slices.Contains(payloadTakers, name) && !strings.HasPrefix(name, "outbox.") {
				for _, a := range n.Args {
					if isPayload(a) {
						at(a, name+"(….Payload)")
					}
				}
			}
			fn, ok := ast.Unparen(n.Fun).(*ast.SelectorExpr)
			if !ok || fn.Sel.Name != "Unmarshal" || len(n.Args) != 2 {
				break
			}
			if x, ok := fn.X.(*ast.Ident); !ok || (x.Name != "termsafe" && x.Name != "json") {
				break
			}
			dst := ast.Unparen(n.Args[1])
			if u, ok := dst.(*ast.UnaryExpr); ok && u.Op == token.AND && isPayload(u.X) {
				break // the stored form kept as it is
			}
			if namesPayload(n.Args[0]) {
				at(n, "Unmarshal(payload, …)")
			}
		}
		return true
	})
	return out
}

// payloadTakers are the functions the stored form may be handed to as it is: its length
// (whether anything is left to send), and writing it back (outbox.* reads it through the
// structs).
var payloadTakers = []string{"len", "json.Marshal", "nonNil"}

// callName is the name of the function n calls: "f" or "pkg.f" ("" for another form).
func callName(n *ast.CallExpr) string {
	fun := ast.Unparen(n.Fun)
	switch g := fun.(type) { // (a generic function: f[T])
	case *ast.IndexExpr:
		fun = g.X
	case *ast.IndexListExpr:
		fun = g.X
	}
	switch fn := fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if x, ok := fn.X.(*ast.Ident); ok {
			return x.Name + "." + fn.Sel.Name
		}
	}
	return ""
}

func TestPayloadMapReadsFindsThem(t *testing.T) {
	src := `package p
func f(op *Op, payload string, row struct{ payload sql.NullString }) {
	_ = op.Payload["k"]
	v, ok := (op.Payload)["k"]
	for k := range op.Payload {}
	var m map[string]any
	termsafe.Unmarshal([]byte(payload), &m)
	json.Unmarshal([]byte(row.payload.String), &m)
	termsafe.Unmarshal([]byte(payload), &op.Payload) // the stored form kept
	termsafe.Unmarshal([]byte(base), &m)              // not the payload
	_ = len(op.Payload)                               // whether anything is left
	_ = op.Base["k"]                                  // not the payload
	applyFields(ctx, tx, gid, op.Payload, now)
	_, _ = json.Marshal(nonNil(op.Payload))           // written back as it is
	_, _ = outbox.PayloadOf[X](op.Payload)
	_, _ = v, ok
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := payloadMapReads(fset, f)
	want := []string{
		"p.go:3: Payload[…]", "p.go:4: Payload[…]", "p.go:5: range ….Payload",
		"p.go:7: Unmarshal(payload, …)", "p.go:8: Unmarshal(payload, …)", "p.go:13: applyFields(….Payload)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("payloadMapReads:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The store does not read the payload of an outbox entry in SQL either (json_extract and
// the other JSON functions on the payload column): it picks the entries by their columns
// in SQL, and by what their payload holds in Go, read as their kind's struct
// (store/payload_match.go). Text searches of the stored form (instr, LIKE, replace: a
// local gid wherever it is) are not reads of a value, and are not checked.
func TestStoreSQLDoesNotReadPayloads(t *testing.T) {
	g := loadModule(t)
	var found []string
	fset := token.NewFileSet()
	for _, p := range payloadReaders {
		info, ok := g.pkgs[p]
		if !ok {
			t.Fatalf("package %s not found", p)
		}
		for _, name := range info.files {
			f, err := parser.ParseFile(fset, filepath.Join(info.dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range payloadSQLReads(fset, f) {
				found = append(found, p+"/"+r)
			}
		}
	}
	report(t, found, "outbox payloads read in SQL (pick the entries by their columns, and read the payload as the kind's struct in Go: opsWhere, payloadIs):")
}

// payloadSQL matches a JSON function of SQLite applied to the payload column.
var payloadSQL = regexp.MustCompile(`(?i)\bjson_\w+\(\s*(\w+\.)?payload\b`)

// payloadSQLReads returns "file:line: literal" for each string literal of f that applies a
// JSON function to the payload column.
func payloadSQLReads(fset *token.FileSet, f *ast.File) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && payloadSQL.MatchString(lit.Value) {
			pos := fset.Position(lit.Pos())
			out = append(out, fmt.Sprintf("%s:%d: %s", filepath.Base(pos.Filename), pos.Line, payloadSQL.FindString(lit.Value)))
		}
		return true
	})
	return out
}

func TestPayloadSQLReadsFindsThem(t *testing.T) {
	src := `package p
const a = "SELECT json_extract(payload, '$.tag') FROM outbox"
const b = ` + "`" + `SELECT 1 FROM outbox o WHERE JSON_EXTRACT( o.payload, '$.x') = ?` + "`" + `
const c = "SELECT id FROM outbox WHERE instr(payload, ?) > 0"
const d = "SELECT value FROM json_each(?)"
const e = "SELECT json_extract(j.value, '$.gid') FROM custom_fields"
const f = "SELECT json_type(payload) FROM outbox"
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := payloadSQLReads(fset, f)
	want := []string{"p.go:2: json_extract(payload", "p.go:3: JSON_EXTRACT( o.payload", "p.go:7: json_type(payload"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("payloadSQLReads:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
