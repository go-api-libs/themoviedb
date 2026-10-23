package edit

import (
	"maps"
	"slices"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// CountReferences counts the references to each schema in components.schemas, wherever in the document they occur. A
// schema nothing refers to is not in the result.
func CountReferences(doc *openapi.Document) map[string]int {
	counts := map[string]int{}

	WalkSchemas(doc, func(s *openapi.Schema) {
		if s.Ref == nil {
			return
		}

		if name, ok := strings.CutPrefix(s.Ref.Identifier, schemaRefPrefix); ok {
			counts[name]++
		}
	})

	return counts
}

// DescribeReferences gives every reference to a schema in descriptions the description listed for it, beside the
// $ref, unless the reference has a description of its own already.
//
// A description says what a schema is used for in one place. Before schemas that describe the same shape are
// consolidated onto one, describing the references to each keeps what each meant where it was used.
//
// It fails, changing nothing, if a name in descriptions is not in components.schemas ([ErrSchemaNotFound]).
func DescribeReferences(doc *openapi.Document, descriptions map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(descriptions)) {
		if _, ok := doc.Components.Schemas[name]; !ok {
			return &ErrSchemaNotFound{Name: name}
		}
	}

	WalkSchemas(doc, func(s *openapi.Schema) {
		if s.Ref == nil || s.Description != "" {
			return
		}

		name, _ := strings.CutPrefix(s.Ref.Identifier, schemaRefPrefix)
		if d, ok := descriptions[name]; ok {
			s.Description = d
		}
	})

	return nil
}

// RemoveUnreferenced removes those of the schemas names from components.schemas that nothing in doc refers to, and
// returns the ones it removed. A schema a discriminator's mapping names is referred to, as much as by a $ref.
//
// It repeats until each of names that remains is referred to, since removing one can leave another without a
// reference. A schema not among names stays, referred to or not: a specification may define one only to document it.
func RemoveUnreferenced(doc *openapi.Document, names ...string) []string {
	var removed []string

	for {
		used := map[string]int{}

		WalkSchemas(doc, func(s *openapi.Schema) {
			if s.Ref != nil {
				if name, ok := strings.CutPrefix(s.Ref.Identifier, schemaRefPrefix); ok {
					used[name]++
				}
			}

			if s.Discriminator == nil {
				return
			}

			for _, v := range s.Discriminator.Mapping {
				if name, ok := strings.CutPrefix(openapi.MappingRef(v.Value), schemaRefPrefix); ok {
					used[name]++
				}
			}
		})

		n := len(removed)

		for _, name := range names {
			if _, ok := doc.Components.Schemas[name]; ok && used[name] == 0 {
				delete(doc.Components.Schemas, name)

				removed = append(removed, name)
			}
		}

		if len(removed) == n {
			return removed
		}
	}
}
