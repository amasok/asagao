package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// The payload of an outbox entry is built from its kind's struct (#89): the code that
// queues, changes or shows an entry goes through outbox.SetPayload / EditPayload /
// MergeFields (domain/outbox/payload.go, which owns the stored form), never sets keys of
// Op.Payload itself, so that a key name or a value type mistyped there is caught by the
// struct. Tests are not checked: they write stored forms on purpose.

// payloadOwner is the package that converts between the structs and the stored form.
const payloadOwner = "internal/domain/outbox"

// payloadOthers are the packages whose own Payload field is not an outbox entry's: the
// CLI's dry-run plan (plannedChange.Payload, which also lists changes made on Asana
// directly, such as add_member, that have no outbox kind).
var payloadOthers = []string{"internal/cli"}

func TestOutboxPayloadsAreBuiltFromTheirStructs(t *testing.T) {
	g := loadModule(t)
	var found []string
	fset := token.NewFileSet()
	for _, p := range g.packages() {
		if p == payloadOwner || contains(payloadOthers, p) {
			continue
		}
		for _, name := range g.pkgs[p].files {
			f, err := parser.ParseFile(fset, filepath.Join(g.pkgs[p].dir, name), nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range payloadWrites(fset, f) {
				found = append(found, p+"/"+w)
			}
		}
	}
	report(t, found, "outbox payloads built or changed key by key (use outbox.SetPayload, EditPayload or MergeFields with the kind's struct, #89):")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// payloadWrites returns "file:line: what" for each write of a Payload field key by key in
// f: a composite literal with a Payload field (Op{Payload: …}) other than the stored form
// of another (Payload: d.Payload), an assignment to x.Payload
// or to x.Payload[k], and delete / maps.Copy / clear on x.Payload. It goes by names only
// (no types): any field called Payload is taken for an entry's.
func payloadWrites(fset *token.FileSet, f *ast.File) []string {
	var out []string
	at := func(n ast.Node, what string) {
		pos := fset.Position(n.Pos())
		out = append(out, fmt.Sprintf("%s:%d: %s", filepath.Base(pos.Filename), pos.Line, what))
	}
	isPayload := func(e ast.Expr) bool {
		s, ok := ast.Unparen(e).(*ast.SelectorExpr)
		return ok && s.Sel.Name == "Payload"
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			for _, e := range n.Elts {
				if kv, ok := e.(*ast.KeyValueExpr); ok {
					// (handing on the stored form of another, Payload: d.Payload, builds nothing)
					if id, ok := kv.Key.(*ast.Ident); ok && id.Name == "Payload" && !isPayload(kv.Value) {
						at(kv, "Payload: …")
					}
				}
			}
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				l = ast.Unparen(l)
				if ix, ok := l.(*ast.IndexExpr); ok && isPayload(ix.X) {
					at(l, "Payload[…] =")
				} else if isPayload(l) {
					at(l, "Payload =")
				}
			}
		case *ast.CallExpr:
			var name string
			switch fn := ast.Unparen(n.Fun).(type) {
			case *ast.Ident:
				name = fn.Name
			case *ast.SelectorExpr:
				if x, ok := fn.X.(*ast.Ident); ok {
					name = x.Name + "." + fn.Sel.Name
				}
			}
			if (name == "delete" || name == "clear" || strings.HasPrefix(name, "maps.")) && len(n.Args) > 0 && isPayload(n.Args[0]) {
				at(n, name+"(….Payload, …)")
			}
		}
		return true
	})
	return out
}

func TestPayloadWritesFindsThem(t *testing.T) {
	src := `package p
func f(op *Op, m map[string]any) {
	_ = Op{Kind: "x", Payload: m}
	op.Payload["k"] = 1
	op.Payload = m
	(op.Payload)["k"], x = 1, 2
	delete(op.Payload, "k")
	maps.Copy(op.Payload, m)
	clear(op.Payload)
	_ = op.Payload["k"]          // a read
	v, ok := op.Payload["k"]     // a read
	maps.Copy(m, op.Payload)     // a read of it
	_ = Op{Kind: "x", Base: m}   // not the payload
	_ = Op{Payload: d.Payload}   // the stored form of another
	_, _ = v, ok
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	got := payloadWrites(fset, f)
	want := []string{
		"p.go:3: Payload: …", "p.go:4: Payload[…] =", "p.go:5: Payload =", "p.go:6: Payload[…] =",
		"p.go:7: delete(….Payload, …)", "p.go:8: maps.Copy(….Payload, …)", "p.go:9: clear(….Payload, …)",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("payloadWrites:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
