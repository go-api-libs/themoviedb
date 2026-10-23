package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/MarkRosemaker/openapi"
	flatten "github.com/MarkRosemaker/openapi-flatten"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "openapi-flatten: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var (
		specPath string
		cfg      flatten.Config
	)

	flag.StringVar(&specPath, "spec", "api/openapi.json", "path to OpenAPI spec file")
	flag.BoolVar(&cfg.MarkOrigin, "mark-origin", false, "mark each schema moved into the components with x-flattened-from")
	flag.Parse()

	doc, err := openapi.LoadFromFile(specPath)
	if err != nil {
		return err
	}

	wasValid := doc.Validate() == nil

	if err := flatten.Document(doc, cfg); err != nil {
		return err
	}

	// Sort responses and components (but not paths to keep the order)
	for _, path := range doc.Paths {
		for _, op := range path.Operations {
			op.Responses.Sort()
		}
	}

	doc.Components.SortMaps()

	if wasValid {
		if err := doc.Validate(); err != nil {
			return fmt.Errorf("produced invalid doc: %w", err)
		}
	}

	if err := doc.WriteToFile(specPath); err != nil {
		return err
	}

	return nil
}
