package cli

import "github.com/amasok/asagao/internal/wire"

// The commands get the adapters from internal/wire, as cmd/asagao gives them.
func init() {
	wiring = Wiring{
		APIClient: wire.APIClient,
		Syncer:    wire.Syncer,
		OpenStore: StoreOpener(wire.OpenStore),
		CheckPAT:  wire.CheckPAT,
	}
}
