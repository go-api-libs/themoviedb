// Command openapi-codegen generates Go client, server, and type code from an OpenAPI spec.
package main

import (
	"flag"
	"fmt"
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

	dirName := filepath.Base(wd)
	dfltPkg := strings.ReplaceAll(strings.ToLower(dirName), "-", "")

	cfg := codegen.Config{}
	flag.StringVar(&cfg.SpecPath, "spec", "api/openapi.json", "path to OpenAPI spec file")
	flag.StringVar(&cfg.OutputDir, "out", filepath.Join("pkg", dfltPkg), "output directory for generated files")
	flag.StringVar(&cfg.PackageName, "pkg", dfltPkg, "Go package name")
	flag.StringVar(&cfg.UserAgent, "agent", "", "User-Agent string for the generated client")
	flag.BoolVar(&cfg.Client, "client", false, "generate client.gen.go and client.gen_test.go")
	flag.BoolVar(&cfg.Server, "server", false, "generate server.gen.go")
	flag.BoolVar(&cfg.JS, "js", false, "generate api.js")
	flag.BoolVar(&cfg.Debug, "debug", false, "generate client in debug mode")
	flag.Parse()

	if !cfg.Client && !cfg.Server && !cfg.JS {
		fmt.Fprintln(os.Stderr, "error: either -client, -server, or -js are required")
		flag.Usage()
		os.Exit(1)
	}

	// Generate types if needed
	cfg.Types = cfg.Client || cfg.Server
	// Add tests if client is generated
	cfg.ClientTest = cfg.Client

	if err := codegen.Generate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "openapi-codegen: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}
}
