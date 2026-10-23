package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/MarkRosemaker/openapi"
	compress "github.com/MarkRosemaker/openapi-compress"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "openapi-compress: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var (
		specPath                      string
		minSimilarity, similarityStep float64
		trimExamples                  int
	)
	flag.StringVar(&specPath, "spec", "api/openapi.json", "path to OpenAPI spec file")
	flag.Float64Var(&minSimilarity, "minsim", 1, "the minimum Jaccard similarity (0..1) for two schemas to be considered for merging; 1.0 means exact equality only (default)")
	flag.Float64Var(&similarityStep, "simstep", .05, "the amount by which the similarity threshold is reduced between rounds when no merges are found at the current threshold; default: 0.05")
	flag.IntVar(&trimExamples, "trim-examples", 0,
		"cap array length in every schema's example to this many representative elements (0 leaves them as they are)")
	flag.Parse()

	doc, err := openapi.LoadFromFile(specPath)
	if err != nil {
		return err
	}

	wasValid := doc.Validate() == nil

	if err := compress.Document(doc, compress.Config{
		MinSimilarity:  minSimilarity,
		SimilarityStep: similarityStep,
		TrimExamples:   trimExamples,
	}); err != nil {
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
