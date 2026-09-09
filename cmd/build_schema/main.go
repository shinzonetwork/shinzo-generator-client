package main

import (
	"flag"
	"io"
	"os"

	"github.com/shinzonetwork/shinzo-generator-client/pkg/chains/evm"
	"github.com/shinzonetwork/shinzo-generator-client/pkg/schema"
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
	case *file != "" && *prefix != "":
		sdl, err = schema.LoadCollectionSDLForChain(c, *file)
	case *file != "":
		sdl, err = schema.LoadCollectionSDL(*file)
	default:
		sdl, err = schema.GetSchemaForChain(c)
	}

	if err != nil {
		return err
	}
	_, err = io.WriteString(stdout, sdl)
	return err
}

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		os.Exit(1)
	}
}
