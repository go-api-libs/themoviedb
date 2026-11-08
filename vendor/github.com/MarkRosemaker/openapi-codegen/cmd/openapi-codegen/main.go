// Command openapi-codegen generates Go client, server, and type code from an OpenAPI spec.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	codegen "github.com/MarkRosemaker/openapi-codegen"
)

func main() {
	wd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}

	switch err := run(os.Args[1:], wd, os.Stderr); {
	case err == nil, errors.Is(err, flag.ErrHelp):
	default:
		_, _ = fmt.Fprintf(os.Stderr, "openapi-codegen: %v\n", err)

		os.Exit(1)
	}
}

// run generates what the command-line args ask for; wd is the working directory, whose name is the default package.
func run(args []string, wd string, stderr io.Writer) error {
	dfltPkg := strings.ReplaceAll(strings.ToLower(filepath.Base(wd)), "-", "")

	fs := flag.NewFlagSet("openapi-codegen", flag.ContinueOnError)
	fs.SetOutput(stderr)

	cfg := codegen.Config{}
	fs.StringVar(&cfg.SpecPath, "spec", "api/openapi.json", "path to OpenAPI spec file")
	fs.StringVar(&cfg.OutputDir, "out", "", "output directory for generated files (default pkg/<package>)")
	fs.StringVar(&cfg.PackageName, "pkg", dfltPkg, "Go package name")
	fs.StringVar(&cfg.UserAgent, "agent", "", "User-Agent string for the generated client")
	fs.BoolVar(&cfg.Client, "client", false, "generate client.gen.go and client.gen_test.go")
	fs.BoolVar(&cfg.Server, "server", false, "generate server.gen.go")
	fs.BoolVar(&cfg.JS, "js", false, "generate api.js")
	fs.BoolVar(&cfg.Debug, "debug", false, "generate client in debug mode")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if cfg.OutputDir == "" {
		cfg.OutputDir = filepath.Join("pkg", cfg.PackageName)
	}

	if !cfg.Client && !cfg.Server && !cfg.JS {
		fs.Usage()
		return errors.New("either -client, -server, or -js are required")
	}

	cfg.Types = cfg.Client || cfg.Server
	cfg.ClientTest = cfg.Client

	if err := codegen.Generate(cfg); err != nil {
		fs.Usage()
		return err
	}

	return nil
}
