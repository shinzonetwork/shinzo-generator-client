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
	listFiles := fs.Bool("list-files", false, "List collection filenames in apply order, one per line")
	prefix := fs.String("prefix", "", "Chain prefix for collection names (e.g. Arbitrum__Mainnet). Defaults to Ethereum__Mainnet if empty.")
	file := fs.String("file", "", "Single collection file to output (e.g. block.graphql). Default: full merged SDL.")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	var sdl string
	switch {
	case *listFiles:
		files, err := schema.ListCollectionFiles()
		if err != nil {
			return err
		}
		for _, f := range files {
			if _, err := fmt.Fprintln(stdout, f); err != nil {
				return err
			}
		}
		return nil
	case *file != "":
		var err error
		if *prefix != "" {
			sdl, err = schema.LoadCollectionSDLForChain(*file, *prefix)
		} else {
			sdl, err = schema.LoadCollectionSDL(*file)
		}
		if err != nil {
			return err
		}
	case *prefix != "":
		sdl = schema.GetSchemaForChain(*prefix)
	default:
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
