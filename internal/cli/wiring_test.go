package cli

import (
	"github.com/spf13/cobra"

	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/wire"
)

// testWiring returns the default production wiring for tests.
func testWiring() Wiring {
	return Wiring{
		MigrateLegacyDirs: config.MigrateLegacyDirs,
		LoadSettings:      config.Load,
		APIClient:         wire.APIClient,
		OpenStore:         StoreOpener(wire.OpenStore, wire.UseCases, wire.Syncer),
		CheckPAT:          wire.CheckPAT,
		Searches:          config.SearchFile{},
	}
}

func newTestRoot(baseURL string) *cobra.Command {
	return newRoot(&app{wiring: testWiring(), baseURL: baseURL})
}
