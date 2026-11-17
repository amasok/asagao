package main

import (
	"github.com/amasok/asagao/internal/cli"
	"github.com/amasok/asagao/internal/wire"
)

func main() {
	cli.Execute(cli.Wiring{
		APIClient: wire.APIClient,
		Syncer:    wire.Syncer,
		OpenStore: wire.OpenStore,
		CheckPAT:  wire.CheckPAT,
	})
}
