package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
)

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("build_schema", flag.ContinueOnError)
	listFiles := fs.Bool("list-files", false, "List collection filenames in apply order, one per line")
	prefix := fs.String("prefix", "", "Chain prefix for collection names (e.g. Arbitrum__Mainnet). Defaults to Ethereum__Mainnet if empty.")
	file := fs.String("file", "", "Single collection file to output (e.g. block.graphql). Default: full merged SDL.")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	p := *prefix
	if p == "" {
		p = evm.DefaultCollectionPrefix
	}
	c := evm.NewCollectionNames(p)

	var sdl string
	var err error
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
		sdl, err = c.MergedSDL()
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
func collectionSDLByFile(c *evm.CollectionNames, file string) (string, error) {
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
