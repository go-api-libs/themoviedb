package render

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
)

// importPaths is every package generated code uses, by the name it is used by. The import section of a generated file
// is written from it (see [writeImports]), not from the template, so a package added to a template must be added here.
var importPaths = map[string]string{
	"base64":   "encoding/base64",
	"bytes":    "bytes",
	"cmp":      "cmp",
	"context":  "context",
	"errors":   "errors",
	"filepath": "path/filepath",
	"fmt":      "fmt",
	"fs":       "io/fs",
	"http":     "net/http",
	"httptest": "net/http/httptest",
	"io":       "io",
	"json":     "encoding/json/v2",
	"jsontext": "encoding/json/jsontext",
	"maps":     "maps",
	"net":      "net",
	"os":       "os",
	"slices":   "slices",
	"slog":     "log/slog",
	"strconv":  "strconv",
	"strings":  "strings",
	"testing":  "testing",
	"time":     "time",
	"url":      "net/url",
	"uuid":     "uuid",

	"api":      "github.com/go-api-libs/api",
	"cassette": "github.com/MarkRosemaker/cassette",
	"civil":    "cloud.google.com/go/civil",
	"jsonutil": "github.com/MarkRosemaker/jsonutil",
	"server":   "github.com/go-api-libs/api/server",
	"types":    "github.com/go-api-libs/types",
	"union":    "github.com/go-api-libs/api/union",
}

// writeImports gives the Go source src the import section it needs: each package of [importPaths] it uses, and no
// other, the standard library's first, then the rest, each group sorted by path. Whatever imports src has are
// replaced: what a file uses is read off its code, so a template needs no import section of its own.
//
// A package is used where its name is the left side of a selector, such as http in http.StatusOK, and the name is
// declared nowhere in the file, so that a variable of the same name does not count.
func writeImports(name string, src []byte) ([]byte, error) {
	fset := token.NewFileSet()

	f, err := parser.ParseFile(fset, name, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	used := map[string]bool{}

	ast.Inspect(f, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			// an identifier the parser could not resolve in the file: a package, or something of the universe scope
			if id, ok := sel.X.(*ast.Ident); ok && id.Obj == nil && importPaths[id.Name] != "" {
				used[id.Name] = true
			}
		}

		return true
	})

	var std, other []string

	for name := range used {
		path := importPaths[name]
		if first, _, _ := strings.Cut(path, "/"); strings.Contains(first, ".") {
			other = append(other, path)
		} else {
			std = append(std, path)
		}
	}

	slices.Sort(std)
	slices.Sort(other)

	var block bytes.Buffer
	if len(std)+len(other) > 0 {
		block.WriteString("\n\nimport (\n")

		for _, path := range std {
			fmt.Fprintf(&block, "\t%q\n", path)
		}

		if len(std) > 0 && len(other) > 0 {
			block.WriteString("\n")
		}

		for _, path := range other {
			fmt.Fprintf(&block, "\t%q\n", path)
		}

		block.WriteString(")")
	}

	// the source without its imports, and the new ones right after the package clause
	out := slices.Clone(src[:fset.Position(f.Name.End()).Offset])
	out = append(out, block.Bytes()...)
	at := fset.Position(f.Name.End()).Offset

	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}

		out = append(out, src[at:fset.Position(gen.Pos()).Offset]...)

		// and the blank lines after it, which the space before the next declaration already holds
		at = fset.Position(gen.End()).Offset
		for at < len(src) && (src[at] == '\n' || src[at] == ' ' || src[at] == '\t') {
			at++
		}
	}

	return append(out, src[at:]...), nil
}
