package ir

import (
	"slices"
	"strconv"
	"unicode"
	"unicode/utf8"

	"github.com/MarkRosemaker/openapi"
	edit "github.com/MarkRosemaker/openapi-edit"
	"github.com/ettle/strcase"
)

// exportComponentNames renames every component schema whose name would make an unexported Go type to its Go-style
// exported form, such as idRequest to IDRequest and error_api_400 to ErrorAPI400, rewriting every reference to it.
//
// A name that is exported already, or a schema with an x-go-name, is left alone. A new name another schema holds or
// takes first gets a number, as the second Date does: Date2.
func exportComponentNames(doc *openapi.Document) error {
	schemas := doc.Components.Schemas

	taken := map[string]bool{}

	var unexported []string

	for name, s := range schemas {
		if goNameOverride(s) != "" || isExported(name) {
			taken[componentGoName(name, s)] = true
		} else {
			unexported = append(unexported, name)
		}
	}

	slices.Sort(unexported)

	to := make(map[string]string, len(unexported))

	for _, name := range unexported {
		base := strcase.ToGoPascal(name)
		if !isExported(base) {
			taken[componentGoName(name, schemas[name])] = true
			continue // nothing to export it as, such as a name of digits alone
		}

		newName := base
		for n := 2; taken[newName]; n++ {
			newName = base + strconv.Itoa(n)
		}

		taken[newName] = true
		to[name] = newName
	}

	if len(to) == 0 {
		return nil
	}

	return edit.RenameSchemas(doc, to)
}

// isExported reports whether name begins with an upper-case letter, as an exported Go identifier does.
func isExported(name string) bool {
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(r)
}
