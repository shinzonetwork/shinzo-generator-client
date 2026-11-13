package main

import (
	"github.com/shinzonetwork/shinzo-generator-client/cmd/block_poster/internal/generator"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

func main() {
	// The runner receives the evm factories directly, so indexing dispatches
	// through injected factories rather than the chain factory registry.
	generator.Main(evm.Factories())
}
