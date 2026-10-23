// Package codegen is the top-level pipeline for openapi-codegen.
package codegen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi"
	"github.com/MarkRosemaker/openapi-codegen/ir"
	"github.com/MarkRosemaker/openapi-codegen/render"
	"github.com/spf13/afero"
)

// Generate runs the full pipeline: parse → flatten → IR → render → write.
func Generate(cfg Config) error {
	if cfg.Spec == nil {
		if cfg.SpecPath == "" {
			return errors.New("SpecPath is required")
		}

		doc, err := openapi.LoadFromFile(cfg.SpecPath)
		if err != nil {
			return err
		}

		cfg.Spec = doc
	}

	if cfg.Interactions == nil {
		pathProvided := cfg.InteractionsPath != ""
		if !pathProvided && cfg.SpecPath != "" {
			cfg.InteractionsPath = filepath.Join(filepath.Dir(cfg.SpecPath), "interactions.json")
		}

		if cfg.InteractionsPath != "" {
			var err error

			cfg.Interactions, err = cassette.InteractionsReadFile(cfg.InteractionsPath)
			if err != nil && (pathProvided || !errors.Is(err, fs.ErrNotExist)) {
				return err
			}
		}
	}

	if cfg.PackageName == "" && cfg.GoFiles() {
		return errors.New("PackageName is required")
	}

	if cfg.OutputFs == nil {
		if cfg.OutputDir == "" {
			cfg.OutputFs = afero.NewOsFs()
		} else {
			cfg.OutputFs = afero.NewBasePathFs(afero.NewOsFs(), cfg.OutputDir)
		}
	}

	if cfg.OutputDir != "" {
		if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", cfg.OutputDir, err)
		}
	}

	if err := cfg.Spec.Validate(); err != nil {
		return fmt.Errorf("invalid spec given: %w", err)
	}

	irDoc, err := ir.FromDocument(cfg.Spec, cfg.PackageName, cfg.UserAgent, cfg.Production, cfg.Debug)
	if err != nil {
		return fmt.Errorf("build IR: %w", err)
	}

	if len(cfg.Interactions) > 0 {
		if err := matchInteractions(irDoc, cfg.Interactions); err != nil {
			return fmt.Errorf("matching interactions: %w", err)
		}
	}

	files, err := render.Files(irDoc, cfg.Generate)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}

	for _, f := range files {
		if err := afero.WriteFile(cfg.OutputFs, f.Name, f.Content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", f.Name, err)
		}
	}

	return nil
}
