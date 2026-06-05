package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/shinzonetwork/shinzo-indexer-client/pkg/schema"
)

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("build_schema", flag.ContinueOnError)
	prefix := fs.String("prefix", "", "Chain prefix for collection names (e.g. Arbitrum__Mainnet). Defaults to Ethereum__Mainnet if empty.")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	var sdl string
	if *prefix != "" {
		sdl = schema.GetSchemaForChain(*prefix)
	} else {
		sdl = schema.GetSchema()
	}

	_, err := fmt.Fprint(stdout, sdl)
	return err
}

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		os.Exit(1)
	}
}
