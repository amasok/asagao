package main

import (
	"github.com/amasok/asagao/internal/cli"
	"github.com/amasok/asagao/internal/config"
	"github.com/amasok/asagao/internal/wire"
)

func main() { cli.Execute(wiring()) }

// wiring is what the commands get from the adapters (every field set).
func wiring() cli.Wiring {
	return cli.Wiring{
		MigrateLegacyDirs: config.MigrateLegacyDirs,
		LoadSettings:      config.Load,
		APIClient:         wire.APIClient,
		Syncer:            wire.Syncer,
		OpenStore:         cli.StoreOpener(wire.OpenStore),
		CheckPAT:          wire.CheckPAT,
		Searches:          config.SearchFile{},
	}
}
