package main

import (
	"github.com/shinzonetwork/shinzo-generator-client/cmd/block_poster/internal/generator"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/tron"
)

func main() {
	// The runner receives the tron factories directly, so indexing dispatches
	// through injected factories with no global adapter state involved.
	generator.Main(tron.Factories())
}
