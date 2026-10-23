package edit

import (
	"errors"
	"slices"

	"github.com/MarkRosemaker/openapi"
)

// ErrNoMatch is returned when no inline schema matches, so there is nothing to extract.
var ErrNoMatch = errors.New("no inline schema matches")

// ExtractSchema moves the inline schemas match accepts into components.schemas, as one schema called name, and
// replaces each with a reference to it.
//
// The first schema match accepts, in the order the document holds them, becomes the component, so match should
// accept only schemas that are the same: every one it accepts is replaced, and what set the others apart is lost.
// Only their descriptions are kept, on the references that replace them, since a description says what a schema is
// used for there, not what it is.
//
// The schemas already in components.schemas are not inline, so match is never asked about them. It is asked about
// schemas within them, and anywhere else in the document. A schema it accepts inside another it accepts is part of
// that one, and moves into the component with it, rather than becoming a reference to the component itself.
//
// It fails, changing nothing, if name is already taken ([ErrSchemaExists]), could not be referenced
// ([ErrInvalidSchemaName]), or if match accepts no schema ([ErrNoMatch]).
func ExtractSchema(doc *openapi.Document, name string, match func(*openapi.Schema) bool) error {
	if !reComponentKey.MatchString(name) {
		return &ErrInvalidSchemaName{Name: name}
	}

	if _, ok := doc.Components.Schemas[name]; ok {
		return &ErrSchemaExists{Name: name}
	}

	named := map[*openapi.Schema]bool{}
	for _, s := range doc.Components.Schemas {
		named[s] = true
	}

	var matches []*openapi.Schema

	WalkSchemas(doc, func(s *openapi.Schema) {
		if s.Ref == nil && !named[s] && match(s) {
			matches = append(matches, s)
		}
	})

	// a match inside another is part of what that one is, and goes with it into the component
	within := map[*openapi.Schema]bool{}

	var mark func(*openapi.Schema)

	mark = func(s *openapi.Schema) {
		if s != nil && !within[s] {
			within[s] = true
			subschemas(s, mark)
		}
	}

	for _, m := range matches {
		subschemas(m, mark)
	}

	matches = slices.DeleteFunc(matches, func(m *openapi.Schema) bool { return within[m] })
	if len(matches) == 0 {
		return ErrNoMatch
	}

	extracted := new(openapi.Schema)
	extracted.Replace(matches[0])
	extracted.Description = ""

	for _, m := range matches {
		m.Replace(&openapi.Schema{
			Description: m.Description,
			Ref:         &openapi.SchemaRef{Identifier: schemaRefPrefix + name, Value: extracted},
		})
	}

	doc.Components.Schemas.Set(name, extracted)

	return nil
}
