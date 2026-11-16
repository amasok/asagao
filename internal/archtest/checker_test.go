package archtest

import (
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// These tests run the checks on made-up graphs, to show that each one fails when it should.
// They use the layer tables of rules_test.go, but their own allowlist (sampleAllowed), so
// that removing a line of allowed when a stage is done does not touch this file.

// fakeGraph builds a graph from the module's packages and their imports. An import that is
// not a key of pkgs is an external package if mods names its module, and standard otherwise.
func fakeGraph(pkgs map[string][]string, mods map[string]string) *graph {
	g := &graph{pkgs: map[string]*pkgInfo{}, std: map[string]bool{}, modules: map[string]string{}}
	for p, imps := range pkgs {
		g.pkgs[p] = &pkgInfo{imports: imps}
	}
	for _, imps := range pkgs {
		for _, imp := range imps {
			if _, ok := pkgs[imp]; ok {
				continue
			}
			if m, ok := mods[imp]; ok {
				g.modules[imp] = m
			} else {
				g.std[imp] = true
			}
		}
	}
	return g
}

// sample is the import graph of the module as it was at stage a2 (only the imports that
// matter here): every line of sampleAllowed happens, and nothing else breaks a rule.
func sample() map[string][]string {
	return map[string][]string{
		"cmd/asana-tui":            {"internal/cli"},
		"internal/app":             {"context", "internal/dates", "internal/domain/task"},
		"internal/dates":           {"time"},
		"internal/domain/task":     nil,
		"internal/asana":           {"net/http", "internal/termsafe"},
		"internal/auth":            {"internal/asana", "internal/auth/tokenstore", "internal/browser", "internal/config", "golang.org/x/oauth2"},
		"internal/auth/tokenstore": {"internal/config", "github.com/zalando/go-keyring"},
		"internal/browser":         {"os/exec"},
		"internal/config":          {"github.com/pelletier/go-toml/v2"},
		"internal/imageview":       {"golang.org/x/sys/unix", "golang.org/x/term"},
		"internal/richtext":        {"github.com/yuin/goldmark", "github.com/yuin/goldmark/ast", "golang.org/x/net/html"},
		"internal/store":           {"internal/asana", "internal/dates", "internal/domain/task", "internal/richtext", "internal/termsafe", "modernc.org/sqlite", "golang.org/x/sys/unix"},
		"internal/sync":            {"internal/asana", "internal/auth", "internal/config", "internal/store", "internal/termsafe", "golang.org/x/oauth2"},
		"internal/termsafe":        {"unicode/utf8"},
		"internal/tui":             {"internal/auth", "internal/dates", "internal/imageview", "internal/richtext", "internal/store", "internal/sync", "charm.land/bubbletea/v2"},
		"internal/mcpserver":       {"internal/app", "internal/auth", "internal/dates", "internal/richtext", "internal/store", "internal/sync", "github.com/modelcontextprotocol/go-sdk/mcp"},
		"internal/cli":             {"internal/app", "internal/asana", "internal/auth", "internal/auth/tokenstore", "internal/browser", "internal/config", "internal/imageview", "internal/mcpserver", "internal/store", "internal/sync", "internal/tui", "github.com/spf13/cobra", "golang.org/x/oauth2", "golang.org/x/term"},
		"internal/archtest":        nil,
	}
}

var sampleMods = map[string]string{
	"golang.org/x/oauth2":                        "golang.org/x/oauth2",
	"github.com/zalando/go-keyring":              "github.com/zalando/go-keyring",
	"github.com/pelletier/go-toml/v2":            "github.com/pelletier/go-toml/v2",
	"golang.org/x/sys/unix":                      "golang.org/x/sys",
	"golang.org/x/term":                          "golang.org/x/term",
	"github.com/yuin/goldmark":                   "github.com/yuin/goldmark",
	"github.com/yuin/goldmark/ast":               "github.com/yuin/goldmark",
	"golang.org/x/net/html":                      "golang.org/x/net",
	"modernc.org/sqlite":                         "modernc.org/sqlite",
	"charm.land/bubbletea/v2":                    "charm.land/bubbletea/v2",
	"github.com/mattn/go-runewidth":              "github.com/mattn/go-runewidth",
	"github.com/modelcontextprotocol/go-sdk/mcp": "github.com/modelcontextprotocol/go-sdk",
	"github.com/spf13/cobra":                     "github.com/spf13/cobra",
	"github.com/evil/lib":                        "github.com/evil/lib",
}

// sampleAllowed is the allowlist of sample: allowed as it was at stage a2.
var sampleAllowed = []violation{
	{"internal/auth", "internal/asana", "d1"},
	{"internal/store", "internal/asana", "d1"},
	{"internal/store", "internal/richtext", "c1"},
	{"internal/sync", "internal/store", "b2, d3"},
	{"internal/sync", "internal/asana", "d1, d3"},
	{"internal/sync", "internal/auth", "d2"},
	{"internal/sync", "internal/config", "d3"},
	{"internal/sync", "golang.org/x/oauth2", "d2"},
	{"internal/tui", "internal/store", "a, c2"},
	{"internal/mcpserver", "internal/store", "a, c2"},
	{"internal/cli", "internal/store", "a, c2, d4"},
	{"internal/cli", "internal/asana", "d4"},
	{"internal/cli", "internal/config", "d4"},
	{"internal/cli", "internal/auth/tokenstore", "d4"},
	{"internal/cli", "golang.org/x/oauth2", "d4"},
}

// failures runs the import checks the way TestDependencyDirection does, with sampleAllowed,
// and returns every line it would report.
func failures(g *graph) []string {
	found := directViolations(g)
	return slices.Concat(unknownPackages(g), lines(newViolations(found, sampleAllowed)), lines(staleAllowances(found, sampleAllowed)), indirectExternal(g, sampleAllowed))
}

func TestCheckerPassesTheSample(t *testing.T) {
	if got := failures(fakeGraph(sample(), sampleMods)); len(got) != 0 {
		t.Fatalf("the sample graph fails:\n%s", strings.Join(got, "\n"))
	}
}

func TestCheckerCatches(t *testing.T) {
	tests := []struct {
		name   string
		change func(m map[string][]string)
		want   []string // each must start one of the failures, and nothing else may fail
	}{
		// A new import into a stdOnly layer also reports the external modules it brings in
		// ("X reaches M: ..."). Cases that are not about that ignore those lines.
		{"domain imports an adapter",
			func(m map[string][]string) { m["internal/domain/task"] = []string{"internal/store"} },
			[]string{"internal/domain/task → internal/store: domain imports only"}},
		{"domain imports the application",
			func(m map[string][]string) { m["internal/dates"] = append(m["internal/dates"], "internal/app") },
			[]string{"internal/dates → internal/app: domain imports only"}},
		{"domain imports os",
			func(m map[string][]string) { m["internal/domain/task"] = []string{"os"} },
			[]string{"internal/domain/task → os: domain does not use"}},
		{"domain imports crypto/rand",
			func(m map[string][]string) { m["internal/dates"] = append(m["internal/dates"], "crypto/rand") },
			[]string{"internal/dates → crypto/rand: domain does not use"}},
		{"domain imports net/http",
			func(m map[string][]string) { m["internal/domain/task"] = []string{"net/http"} },
			[]string{"internal/domain/task → net/http: domain does not use"}},
		{"a new domain package imports an adapter",
			func(m map[string][]string) { m["internal/domain/outbox"] = []string{"internal/store"} },
			[]string{"internal/domain/outbox → internal/store: domain imports only"}},
		{"app imports the store",
			func(m map[string][]string) { m["internal/app"] = append(m["internal/app"], "internal/store") },
			[]string{"internal/app → internal/store: the application imports only"}},
		{"app imports richtext",
			func(m map[string][]string) { m["internal/app"] = append(m["internal/app"], "internal/richtext") },
			[]string{"internal/app → internal/richtext: the application imports only"}},
		{"app imports sync",
			func(m map[string][]string) { m["internal/app"] = append(m["internal/app"], "internal/sync") },
			[]string{"internal/app → internal/sync: app and sync do not import each other"}},
		{"sync imports app",
			func(m map[string][]string) { m["internal/sync"] = append(m["internal/sync"], "internal/app") },
			[]string{"internal/sync → internal/app: app and sync do not import each other"}},
		{"app imports an entry point",
			func(m map[string][]string) { m["internal/app"] = append(m["internal/app"], "internal/tui") },
			[]string{"internal/app → internal/tui: the application imports only"}},
		{"an adapter imports another adapter",
			func(m map[string][]string) {
				m["internal/richtext"] = append(m["internal/richtext"], "internal/config")
			},
			[]string{"internal/richtext → internal/config: an adapter imports only"}},
		{"an adapter imports the application",
			func(m map[string][]string) { m["internal/store"] = append(m["internal/store"], "internal/app") },
			[]string{"internal/store → internal/app: an adapter imports only"}},
		{"an adapter imports an entry point",
			func(m map[string][]string) { m["internal/asana"] = append(m["internal/asana"], "internal/cli") },
			[]string{"internal/asana → internal/cli: an adapter imports only"}},
		{"tokenstore imports auth (the exception goes one way)",
			func(m map[string][]string) {
				m["internal/auth/tokenstore"] = append(m["internal/auth/tokenstore"], "internal/auth")
			},
			[]string{"internal/auth/tokenstore → internal/auth: an adapter imports only"}},
		{"an entry point imports asana",
			func(m map[string][]string) {
				m["internal/mcpserver"] = append(m["internal/mcpserver"], "internal/asana")
			},
			[]string{"internal/mcpserver → internal/asana: entry points import only the adapters"}},
		{"an entry point imports config",
			func(m map[string][]string) { m["internal/tui"] = append(m["internal/tui"], "internal/config") },
			[]string{"internal/tui → internal/config: entry points import only the adapters"}},
		{"tui imports cli",
			func(m map[string][]string) { m["internal/tui"] = append(m["internal/tui"], "internal/cli") },
			[]string{"internal/tui → internal/cli: only cli imports other entry points"}},
		{"mcpserver imports tui",
			func(m map[string][]string) { m["internal/mcpserver"] = append(m["internal/mcpserver"], "internal/tui") },
			[]string{"internal/mcpserver → internal/tui: only cli imports other entry points"}},
		{"termsafe imports the module",
			func(m map[string][]string) { m["internal/termsafe"] = append(m["internal/termsafe"], "internal/dates") },
			[]string{"internal/termsafe → internal/dates: termsafe imports only the standard library"}},
		{"shared types import an adapter",
			func(m map[string][]string) { m["internal/readmodel"] = []string{"internal/store"} },
			[]string{"internal/readmodel → internal/store: shared types import only"}},
		{"something imports the wiring",
			func(m map[string][]string) {
				m["internal/wire"] = []string{"internal/store"}
				m["internal/cli"] = append(m["internal/cli"], "internal/wire")
			},
			[]string{"internal/cli → internal/wire: nothing imports the wiring"}},
		{"something imports archtest",
			func(m map[string][]string) { m["cmd/asana-tui"] = append(m["cmd/asana-tui"], "internal/archtest") },
			[]string{"cmd/asana-tui → internal/archtest: packages with only tests are not imported"}},
		{"a package without a layer",
			func(m map[string][]string) { m["internal/newthing"] = nil },
			[]string{"internal/newthing"}},
		{"an external module where external does not list it",
			func(m map[string][]string) { m["internal/store"] = append(m["internal/store"], "golang.org/x/oauth2") },
			[]string{"internal/store → golang.org/x/oauth2: external modules are used only"}},
		{"an unknown external module",
			func(m map[string][]string) { m["internal/tui"] = append(m["internal/tui"], "github.com/evil/lib") },
			[]string{"internal/tui → github.com/evil/lib: external modules are used only"}},
		{"app uses an external module",
			func(m map[string][]string) {
				m["internal/app"] = append(m["internal/app"], "github.com/pelletier/go-toml/v2")
			},
			[]string{"internal/app → github.com/pelletier/go-toml/v2: external modules are used only"}},
		{"app reaches an external module through the domain",
			func(m map[string][]string) { m["internal/dates"] = append(m["internal/dates"], "github.com/evil/lib") },
			[]string{
				"internal/dates → github.com/evil/lib: external modules are used only",
				"internal/app reaches github.com/evil/lib: internal/app → internal/dates → github.com/evil/lib",
			}},
		{"an allowed import is gone (sync no longer imports config)",
			func(m map[string][]string) {
				m["internal/sync"] = slices.DeleteFunc(m["internal/sync"], func(s string) bool { return s == "internal/config" })
			},
			[]string{"internal/sync → internal/config: d3"}},
		{"an allowed external module is gone",
			func(m map[string][]string) {
				m["internal/cli"] = slices.DeleteFunc(m["internal/cli"], func(s string) bool { return s == "golang.org/x/oauth2" })
			},
			[]string{"internal/cli → golang.org/x/oauth2: d4"}},
		{"sync stops importing config but reaches go-toml another way",
			func(m map[string][]string) {
				m["internal/sync"] = slices.DeleteFunc(m["internal/sync"], func(s string) bool { return s == "internal/config" })
				m["internal/sync"] = append(m["internal/sync"], "internal/domain/task")
				m["internal/domain/task"] = []string{"internal/config"}
			},
			[]string{
				"internal/sync → internal/config: d3",
				"internal/domain/task → internal/config: domain imports only",
				"internal/sync reaches github.com/pelletier/go-toml/v2: internal/sync → internal/domain/task → internal/config → github.com/pelletier/go-toml/v2",
				"internal/domain/task reaches github.com/pelletier/go-toml/v2: internal/domain/task → internal/config → github.com/pelletier/go-toml/v2",
				"internal/app reaches github.com/pelletier/go-toml/v2: internal/app → internal/domain/task → internal/config → github.com/pelletier/go-toml/v2",
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := sample()
			tt.change(m)
			got := failures(fakeGraph(m, sampleMods))
			for _, w := range tt.want {
				if !slices.ContainsFunc(got, func(g string) bool { return strings.HasPrefix(g, w) }) {
					t.Errorf("missing failure %q", w)
				}
			}
			wantReach := slices.ContainsFunc(tt.want, func(w string) bool { return strings.Contains(w, " reaches ") })
			for _, g := range got {
				if !wantReach && strings.Contains(g, " reaches ") {
					continue
				}
				if !slices.ContainsFunc(tt.want, func(w string) bool { return strings.HasPrefix(g, w) }) {
					t.Errorf("unexpected failure %q", g)
				}
			}
		})
	}
}

// TestCheckerAllowsTheRules checks imports that the rules allow and that the sample does not
// have.
func TestCheckerAllowsTheRules(t *testing.T) {
	m := sample()
	m["internal/domain/outbox"] = []string{"internal/domain/task", "time"}
	m["internal/asana/model"] = []string{"time"}
	m["internal/readmodel"] = []string{"internal/asana/model", "internal/domain/task", "internal/termsafe"}
	m["internal/wire"] = []string{"internal/store", "internal/asana", "internal/config", "internal/app", "internal/sync"}
	m["cmd/asana-tui"] = append(m["cmd/asana-tui"], "internal/wire")
	m["internal/app"] = append(m["internal/app"], "internal/asana/model", "internal/readmodel", "internal/termsafe", "internal/domain/outbox")
	m["internal/sync"] = append(m["internal/sync"], "internal/domain/outbox", "internal/asana/model")
	m["internal/store"] = append(m["internal/store"], "internal/readmodel", "internal/asana/model")
	m["internal/tui"] = append(m["internal/tui"], "internal/app", "internal/readmodel", "internal/browser", "internal/termsafe")
	if got := failures(fakeGraph(m, sampleMods)); len(got) != 0 {
		t.Fatalf("allowed imports fail:\n%s", strings.Join(got, "\n"))
	}
}

func TestUnusedExternal(t *testing.T) {
	g := fakeGraph(map[string][]string{
		"internal/store":     {"modernc.org/sqlite"},
		"internal/imageview": {"golang.org/x/term"},
	}, sampleMods)
	got := unusedExternal(g)
	for _, w := range []string{
		`external["golang.org/x/sys"] lists internal/store, which does not import it`,
		`external["golang.org/x/sys"] lists internal/imageview, which does not import it`,
		`external["github.com/spf13/cobra"] lists internal/cli, which does not import it`, // no such package in g
	} {
		if !slices.Contains(got, w) {
			t.Errorf("missing %q", w)
		}
	}
	for _, g := range got {
		if strings.Contains(g, `"modernc.org/sqlite"`) || strings.Contains(g, `"golang.org/x/term"] lists internal/imageview`) {
			t.Errorf("reported a module in use: %q", g)
		}
	}
}

func TestClockUses(t *testing.T) {
	src := `package p

import (
	"time"
	tm "time"
)

var d time.Duration // a type is fine

func f(now time.Time) time.Time {
	_ = time.Now()
	_ = tm.Since(now)
	<-time.After(d)
	return now.Add(time.Hour)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := clockUses(fset, f)
	want := []string{"p.go:11: time.Now", "p.go:12: time.Since", "p.go:13: time.After"}
	if !slices.Equal(got, want) {
		t.Errorf("clockUses = %q, want %q", got, want)
	}

	f, err = parser.ParseFile(fset, "q.go", "package p\n\nimport . \"time\"\n\nvar _ = Now()\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := clockUses(fset, f); !slices.Equal(got, []string{"q.go:3: dot import of time"}) {
		t.Errorf("clockUses with a dot import = %q", got)
	}
}
