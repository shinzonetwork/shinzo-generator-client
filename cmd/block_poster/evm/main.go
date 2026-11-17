package main

import (
	"github.com/shinzonetwork/shinzo-generator-client/cmd/block_poster/internal/generator"
	_ "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

func main() {
	generator.Main()
}
