package archtest

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// What a store file would have to contain to be caught by TestStoreReadsPayloadsThroughTheirStructs.
func TestAttackPayloadMapReadsCatchesTheForms(t *testing.T) {
	cases := map[string]string{
		"index read":       `_ = op.Payload["k"]`,
		"index write":      `op.Payload["k"] = 1`,
		"index of copy":    `v, _ := o.Payload["k"].(string); _ = v`,
		"nested selector":  `_ = x.y.op.Payload["k"]`,
		"range":            `for k, v := range op.Payload { _, _ = k, v }`,
		"range key only":   `for k := range d.Payload { _ = k }`,
		"termsafe decode":  `var m map[string]any; termsafe.Unmarshal([]byte(payload), &m)`,
		"json decode":      `var m map[string]any; json.Unmarshal([]byte(p.payload), &m)`,
		"decode into ptr":  `m := map[string]any{}; termsafe.Unmarshal([]byte(rawPayload), &m)`,
		"decode via param": `var m map[string]any; _ = termsafe.Unmarshal(payloadBytes, &m)`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			src := "package p\nfunc f() {\n" + body + "\n}\n"
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := payloadMapReads(fset, f); len(got) != 1 {
				t.Errorf("payloadMapReads = %v, want exactly one finding", got)
			}
		})
	}
}

// What stays allowed: the stored form kept as it is, a length, other columns.
func TestAttackPayloadMapReadsAllowsStoredForm(t *testing.T) {
	src := `package p
func f(op *Op, d *Discard, base string) {
	termsafe.Unmarshal([]byte(payload), &op.Payload)
	json.Unmarshal([]byte(payload), &d.Payload)
	_ = len(op.Payload) == 0
	_ = op.Payload == nil
	op.Payload = map[string]any{}
	var b map[string]any
	termsafe.Unmarshal([]byte(base), &b)
	_ = op.Base["k"]
	_ = op.Conflict["k"]
}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	if got := payloadMapReads(fset, f); len(got) != 0 {
		t.Errorf("payloadMapReads = %s, want none", strings.Join(got, "; "))
	}
}

// The check runs over the store package as it is, and finds the package's files.
func TestAttackPayloadReadersCoverTheStore(t *testing.T) {
	g := loadModule(t)
	info, ok := g.pkgs["internal/store"]
	if !ok || len(info.files) < 20 {
		t.Fatalf("internal/store files seen by the check: %d (ok=%v)", len(info.files), ok)
	}
	for _, f := range info.files {
		if strings.HasSuffix(f, "_test.go") {
			t.Errorf("test file %s is in the checked set", f)
		}
	}
}
