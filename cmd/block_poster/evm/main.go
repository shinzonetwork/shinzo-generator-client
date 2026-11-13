package main

import (
	"github.com/shinzonetwork/shinzo-generator-client/cmd/block_poster/internal/generator"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

func main() {
	// Registration is explicit: the evm adapter's factories enter the chain
	// factory registry here, before the runner dispatches through it.
	evm.Register()
	generator.Main()
}
