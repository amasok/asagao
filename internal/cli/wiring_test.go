package cli

import (
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/wire"
)

// The commands get the adapters from internal/wire, as cmd/asagao gives them.
func init() {
	wiring = Wiring{
		MigrateLegacyDirs: config.MigrateLegacyDirs,
		LoadSettings:      config.Load,
		APIClient:         wire.APIClient,
		Syncer:            wire.Syncer,
		OpenStore:         StoreOpener(wire.OpenStore),
		CheckPAT:          wire.CheckPAT,
	}
}
