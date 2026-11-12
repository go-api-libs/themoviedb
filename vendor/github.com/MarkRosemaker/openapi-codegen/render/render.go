// Package render generates Go source files from an IR document using embedded templates.
package render

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/MarkRosemaker/openapi-codegen/config"
	"github.com/MarkRosemaker/openapi-codegen/ir"
	gofumpt "mvdan.cc/gofumpt/format"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

// templateSub is the templates/ subdirectory of the embedded FS, extracted at init.
var templateSub fs.FS

func init() {
	var err error

	templateSub, err = fs.Sub(templateFS, "templates")
	if err != nil {
		panic("openapi-codegen: templates directory missing from embedded FS: " + err.Error())
	}
}

// File represents a single rendered output file.
type File struct {
	Name    string
	Content []byte
}

// Files renders all embedded templates against the IR document.
func Files(doc *ir.Document, g config.Generate) ([]File, error) {
	return FilesFromFS(templateSub, doc, g)
}

// FilesFromFS renders all *.tmpl files found in fsys against the IR document.
// The output file name is the template name minus the ".tmpl" suffix.
func FilesFromFS(fsys fs.FS, doc *ir.Document, g config.Generate) ([]File, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}

	var files []File
	for _, e := range entries {
		name := e.Name()
		outName, isTpl := strings.CutSuffix(name, ".tmpl")
		if e.IsDir() || !isTpl || !g.ShouldGenerate(outName) {
			continue
		}

		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}

		var rendered []byte
		if filepath.Ext(outName) == ".go" {
			rendered, err = RenderTemplate(name, string(data), doc)
		} else {
			rendered, err = renderText(name, string(data), doc)
		}

		if err != nil {
			return nil, fmt.Errorf("template %s: %w", name, err)
		}

		// a template with nothing to write, such as tests of types without any to test, is no file
		if rendered == nil {
			continue
		}

		files = append(files, File{Name: outName, Content: rendered})
	}

	return files, nil
}

// RenderTemplate executes a template string against the IR document, writes the imports the result uses (see
// [writeImports]), and formats it with gofumpt.
func RenderTemplate(tmplName, tmplContent string, doc *ir.Document) ([]byte, error) {
	tmpl, err := template.New(tmplName).Funcs(templateFuncs()).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, doc); err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}

	if len(bytes.TrimSpace(buf.Bytes())) == 0 {
		return nil, nil
	}

	// the imports the code uses, from a fixed table, rather than goimports' guesses from what it finds installed
	processed, err := writeImports(tmplName, buf.Bytes())
	if err != nil {
		return buf.Bytes(), fmt.Errorf("imports: %w\n---\n%s", err, buf.String())
	}

	// gofumpt: stricter formatting on top.
	return gofumpt.Source(processed, gofumpt.Options{
		LangVersion: "go1.25",
		ModulePath:  "github.com/MarkRosemaker/openapi-codegen",
	})
}

// renderText executes a template string against the IR document without any
// language-specific post-processing. Used for non-Go output files (e.g. .js).
func renderText(tmplName, tmplContent string, doc *ir.Document) ([]byte, error) {
	tmpl, err := template.New(tmplName).Funcs(templateFuncs()).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	buf := bytes.Buffer{}
	if err := tmpl.Execute(&buf, doc); err != nil {
		return nil, fmt.Errorf("execute: %w", err)
	}

	return buf.Bytes(), nil
}
