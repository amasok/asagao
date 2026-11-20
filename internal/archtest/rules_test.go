// Package archtest checks the direction of the dependencies between the packages of this
// module, as docs/design/architecture.md ("依存の決まり") lays it out. It has only test
// files, so nothing imports it.
//
// This file holds the tables: which layer each package is in, which adapters and entry
// points may import each other, where each external module may be used, and the
// violations let through for now (allowed: none since the end of Issue #69's migration).
// deps_test.go reads the module with `go list` and checks it against these tables.
//
// When a check fails:
//   - A new package: add it to layers (architecture.md, "パッケージごとの置き場所").
//   - A new external module: add it to external with the packages that may use it
//     (ADR 0004). Only adapters and entry points may use one.
//   - A new import that breaks a rule: move the code instead of adding to allowed.
//   - An unused external entry: the package no longer uses the module, so delete it.
//   - A stale allowance: the violation is gone, so delete its line from allowed and mark
//     the row in architecture.md ("今の import と、それぞれの扱い") as done. (allowed is
//     empty now; keep it so.)
package archtest

// layer is one of the layers of docs/design/architecture.md ("層").
type layer string

const (
	layerDomain  layer = "domain"  // rule 1: the standard library and other domain packages
	layerApp     layer = "app"     // rule 2: domain, shared types, termsafe
	layerAdapter layer = "adapter" // rule 3: domain, shared types, termsafe, its own external modules
	layerEntry   layer = "entry"   // rules 4 and 5: app, domain, shared types, termsafe, entryAdapters
	layerWire    layer = "wire"    // rule 6: anything
	layerShared  layer = "shared"  // types only (asana/model, readmodel): the standard library
	layerCommon  layer = "common"  // termsafe, xdg: the standard library only
	layerTest    layer = "test"    // packages with only tests: nothing may import them
	// testguard: the standard library only, imported by the tests of every package
	// (guard_test.go) and by nothing else.
	layerTestSupport layer = "testsupport"
)

// layers assigns every package of the module to a layer. A key ending in "/..." covers the
// directory and everything below it. Packages that do not exist yet (asana/model,
// readmodel, wire) are listed so that they land in the right layer when they are made.
var layers = map[string]layer{
	"internal/domain/...": layerDomain,
	"internal/dates":      layerDomain,

	"internal/app":  layerApp,
	"internal/sync": layerApp,

	"internal/store":           layerAdapter,
	"internal/asana":           layerAdapter,
	"internal/auth":            layerAdapter,
	"internal/auth/tokenstore": layerAdapter,
	"internal/richtext":        layerAdapter,
	"internal/config":          layerAdapter,
	"internal/imageview":       layerAdapter,
	"internal/browser":         layerAdapter,

	"internal/asana/model": layerShared,
	"internal/readmodel":   layerShared,
	"internal/ui/format":   layerShared,
	"internal/settings":    layerShared,
	"internal/repoctx":     layerShared,

	"internal/termsafe": layerCommon,
	"internal/xdg":      layerCommon,

	// The real-Asana check drives the asagao binary from outside: standard library only.
	"scripts/realcheck": layerCommon,

	"internal/tui":       layerEntry,
	"internal/cli":       layerEntry,
	"internal/mcpserver": layerEntry,

	"internal/wire": layerWire,
	"cmd/asagao":    layerWire,

	"internal/archtest": layerTest,

	"internal/testguard": layerTestSupport,
}

// adapterExceptions are the adapters that may import other adapters (rule 3): the parts of
// authentication, and their settings.
var adapterExceptions = map[string][]string{
	"internal/auth":            {"internal/auth/tokenstore", "internal/config", "internal/browser"},
	"internal/auth/tokenstore": {"internal/config"},
}

// displayOnly are the shared packages that hold only how the front ends word things. The
// adapters do not import them (Issue #82): the store keeps values (readmodel's, domain's),
// not display text, so rewording the display does not change what it reads back.
var displayOnly = []string{"internal/ui/format"}

// entryAdapters are the adapters that entry points may import (rule 4). store, asana,
// config and tokenstore are left to the wiring.
var entryAdapters = []string{"internal/auth", "internal/richtext", "internal/imageview", "internal/browser"}

// entryToEntry are the entry points that may import other entry points (rule 5): cli starts
// the TUI and the MCP server.
var entryToEntry = map[string][]string{
	"internal/cli": {"internal/tui", "internal/mcpserver"},
}

// external lists, for each external module, the packages that may import it directly
// (rule 7, ADR 0004, and the "使ってよい外部ライブラリ" column of architecture.md). Only
// adapters and entry points may appear here; domain, app, shared and common packages may
// not use an external module even indirectly. Each entry must be in use: when a package stops
// using a module, delete it here, so that the permission does not stay behind.
var external = map[string][]string{
	"modernc.org/sqlite":              {"internal/store"},
	"golang.org/x/sys":                {"internal/store", "internal/imageview"},
	"golang.org/x/term":               {"internal/imageview", "internal/cli"},
	"github.com/zalando/go-keyring":   {"internal/auth/tokenstore"},
	"github.com/pelletier/go-toml/v2": {"internal/config"},
	"github.com/yuin/goldmark":        {"internal/richtext"},
	"golang.org/x/net":                {"internal/richtext"},
	"golang.org/x/oauth2":             {"internal/auth"},

	"charm.land/bubbletea/v2":         {"internal/tui", "internal/cli"},
	"charm.land/bubbles/v2":           {"internal/tui"},
	"charm.land/lipgloss/v2":          {"internal/tui"},
	"charm.land/glamour/v2":           {"internal/tui"},
	"github.com/charmbracelet/x/ansi": {"internal/tui"},
	"github.com/mattn/go-runewidth":   {"internal/cli"},
	"github.com/spf13/cobra":          {"internal/cli"},

	"github.com/modelcontextprotocol/go-sdk": {"internal/mcpserver", "internal/cli"},
}

// violation is an import that breaks a rule. To is a package of this module, or the path
// of an external module.
type violation struct {
	From, To string
	Why      string // the rule it breaks; in allowed, the stage that removes it and how
}

// allowed are the violations that are let through for now, each with the stage that
// removes it. The migration of Issue #69 removed them all (the last one, sync → store, in
// e1), so the list is empty, as ADR 0007 makes it the condition for the end of the
// migration. Keep it so: move the code rather than add a line here. The check fails on a
// violation that is not listed here, and on a line here that no longer happens, so that a
// fixed violation cannot come back.
//
// The imports that the rules allow (the rows of architecture.md, "今の import と、それぞれの
// 扱い", marked "許す") are not here: layers, adapterExceptions, entryAdapters and
// entryToEntry let them through.
var allowed = []violation{}
