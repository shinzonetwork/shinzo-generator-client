package main

import (
	"github.com/shinzonetwork/shinzo-generator-client/cmd/block_poster/internal/generator"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/cosmos"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

func main() {
	// The runner receives the cosmos-evm factories directly, so indexing
	// dispatches through injected factories with no global adapter state. The
	// EVM half of each block comes from the evm family, wired here because
	// only cmd/ binaries may import the evm adapter.
	generator.Main(cosmos.Factories(evm.Factories()))
}
