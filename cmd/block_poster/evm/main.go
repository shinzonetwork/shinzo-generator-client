package main

import (
	"github.com/shinzonetwork/shinzo-generator-client/cmd/block_poster/internal/app"

	// The adapter registration is an init() side effect, so the import must
	// live in each per-chain main package; the runner stays chain-agnostic.
	_ "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

func main() {
	app.Main()
}
