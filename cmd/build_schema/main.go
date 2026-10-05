package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/shinzonetwork/shinzo-generator-client/config"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"

	// The blank import wires the default chain adapter into the registry;
	// everything below resolves through the pkg/chains interfaces.
	_ "github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("build_schema", flag.ContinueOnError)
	listFiles := fs.Bool("list-files", false, "List collection filenames in apply order, one per line")
	chain := fs.String("chain", "", "Chain name for collection prefixes (e.g. Arbitrum). Defaults to the adapter's default when empty.")
	network := fs.String("network", "", "Network name for collection prefixes (e.g. Mainnet). Defaults to the adapter's default when empty.")
	adapter := fs.String("adapter", "", "Chain adapter to resolve collections from. Defaults to the built-in default when empty.")
	file := fs.String("file", "", "Single collection file to output (e.g. block.graphql). Default: full merged SDL.")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	cfg := &config.Config{Chain: config.ChainConfig{
		Name:    *chain,
		Network: *network,
		Adapter: *adapter,
	}}
	c, err := chains.NewCollections(cfg)
	if err != nil {
		return err
	}

	var sdl string
	switch {
	case *listFiles:
		files, err := c.CollectionFiles()
		if err != nil {
			return err
		}
		for _, f := range files {
			if _, err := io.WriteString(stdout, f.File+"\n"); err != nil {
				return err
			}
		}
		return nil
	case *file != "":
		sdl, err = collectionSDLByFile(c, *file)
	default:
		sdl, err = schema.LoadSchemaSDLForChain(c)
	}

	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, sdl)
	return err
}

// collectionSDLByFile looks up a single collection file's SDL from the chain's
// ordered pairs, so the CLI resolves every file through the same source as
// schema application and reports a clear error on unknown names.
func collectionSDLByFile(c chains.Collections, file string) (string, error) {
	files, err := c.CollectionFiles()
	if err != nil {
		return "", err
	}
	for _, f := range files {
		if f.File == file {
			return f.SDL, nil
		}
	}
	return "", fmt.Errorf("collection file %q not found for prefix %s", file, c.Prefix())
}
