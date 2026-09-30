package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/solana"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
)

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("build_schema", flag.ContinueOnError)
	listFiles := fs.Bool("list-files", false, "List collection filenames in apply order, one per line")
	prefix := fs.String("prefix", "", "Chain prefix for collection names (e.g. Arbitrum__Mainnet, Solana__Devnet). Defaults to the adapter's default prefix if empty.")
	file := fs.String("file", "", "Single collection file to output (e.g. block.graphql). Default: full merged SDL.")
	adapter := fs.String("adapter", config.DefaultChainAdapter, "Chain adapter whose schema set to use: evm (default) or solana")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	c, err := collectionsForAdapter(*adapter, *prefix)
	if err != nil {
		return err
	}

	var sdl string
	switch {
	case *listFiles:
		files, err := schema.ListCollectionFiles(c)
		if err != nil {
			return err
		}
		for _, f := range files {
			if _, err := io.WriteString(stdout, f+"\n"); err != nil {
				return err
			}
		}
		return nil
	case *file != "":
		sdl, err = schema.LoadCollectionSDLForChain(c, *file)
	default:
		sdl, err = schema.GetSchemaForChain(c)
	}

	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, sdl)
	return err
}

// collectionsForAdapter builds the chains.Collections for the requested
// adapter, applying each adapter's default prefix when none is given.
// Binaries are composition roots: importing the adapters here is what runs
// their RegisterChain init() calls.
func collectionsForAdapter(adapter, prefix string) (chains.Collections, error) {
	switch adapter {
	case config.DefaultChainAdapter, "":
		p := prefix
		if p == "" {
			p = evm.DefaultCollectionPrefix
		}
		return evm.NewCollectionNames(p), nil
	case solana.AdapterName:
		p := prefix
		if p == "" {
			p = solana.DefaultCollectionPrefix
		}
		return solana.NewCollectionNames(p), nil
	default:
		return nil, fmt.Errorf("unknown adapter %q: supported adapters are %q and %q", adapter, config.DefaultChainAdapter, solana.AdapterName)
	}
}

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		os.Exit(1)
	}
}
