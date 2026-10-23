// Package edit provides structural edits to an OpenAPI document — changes
// where touching one place obliges you to touch several others.
package edit

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// schemaRefPrefix is the start of every reference to a component schema.
const schemaRefPrefix = "#/components/schemas/"

// reComponentKey is the pattern the OpenAPI specification requires of keys
// under components.
//
// It matters here beyond validity: a name containing "/" would produce a
// reference that resolves somewhere else entirely, and one containing a space
// would produce a reference that does not resolve at all.
//
// See https://spec.openapis.org/oas/v3.1.0#components-object
var reComponentKey = regexp.MustCompile(`^[a-zA-Z0-9.\-_]+$`)

// ErrSchemaNotFound is returned when the schema to rename is not in
// components.schemas.
type ErrSchemaNotFound struct{ Name string }

func (e *ErrSchemaNotFound) Error() string {
	return fmt.Sprintf("schema %q not found in components.schemas", e.Name)
}

// ErrSchemaExists is returned when the new name is already taken by another
// schema. Renaming onto it would silently merge two definitions into one.
type ErrSchemaExists struct{ Name string }

func (e *ErrSchemaExists) Error() string {
	return fmt.Sprintf("schema %q already exists in components.schemas", e.Name)
}

// ErrInvalidSchemaName is returned when the new name is not a valid key under
// components, and so could not be referenced.
type ErrInvalidSchemaName struct{ Name string }

func (e *ErrInvalidSchemaName) Error() string {
	return fmt.Sprintf("schema name %q must match %s", e.Name, reComponentKey)
}

// RenameSchema renames a schema in components.schemas and rewrites every
// reference to it, wherever in the document that reference occurs.
//
// The schema keeps its position among the components, so renaming produces a
// one-line change rather than reordering the section.
//
// Renaming a schema to its current name does nothing and reports no error.
// Otherwise it fails, changing nothing, if:
//
//   - oldName is not in components.schemas ([ErrSchemaNotFound]);
//   - newName is already taken ([ErrSchemaExists]) — renaming onto an existing
//     schema would silently discard one of two different definitions, and
//     point every reference to whichever survived;
//   - newName could not be referenced ([ErrInvalidSchemaName]).
func RenameSchema(doc *openapi.Document, oldName, newName string) error {
	return RenameSchemas(doc, map[string]string{oldName: newName})
}

// RenameSchemas is [RenameSchema] for many schemas at once: it renames each key of to that key's value, and
// rewrites every reference to it.
//
// The renames happen together, so a new name may be one another key is giving up, and two schemas can swap names. It
// walks the document a fixed number of times however many schemas it renames, where calling RenameSchema for each
// would walk it once per schema.
//
// It fails, changing nothing, for the reasons RenameSchema does, and with [ErrSchemaExists] when two keys would take
// the same new name.
func RenameSchemas(doc *openapi.Document, to map[string]string) error {
	schemas := doc.Components.Schemas

	taken := map[string]bool{}

	for _, oldName := range slices.Sorted(maps.Keys(to)) {
		if _, ok := schemas[oldName]; !ok {
			return &ErrSchemaNotFound{Name: oldName}
		}

		newName := to[oldName]
		if oldName == newName {
			continue
		}

		if !reComponentKey.MatchString(newName) {
			return &ErrInvalidSchemaName{Name: newName}
		}

		next, renamed := to[newName]
		if _, exists := schemas[newName]; exists && (!renamed || next == newName) || taken[newName] {
			return &ErrSchemaExists{Name: newName}
		}

		taken[newName] = true
	}

	to = withoutUnchanged(to)
	if len(to) == 0 {
		return nil
	}

	addImplicitMappings := implicitMappings(doc, to)

	moved := make(map[string]*openapi.Schema, len(to))
	for oldName := range to {
		moved[to[oldName]] = schemas[oldName]
		delete(schemas, oldName)
	}

	// Assign directly rather than through Set: the schema carries its own
	// ordering index, so moving the value to a new key keeps it where it was,
	// while Set would move it to the end of the section.
	maps.Copy(schemas, moved)

	WalkSchemas(doc, func(s *openapi.Schema) {
		if newName, ok := renamedRef(s, to); ok {
			s.Ref.Identifier = schemaRefPrefix + newName
		}
	})

	rewriteMappings(doc, to)
	addImplicitMappings()

	return nil
}

// withoutUnchanged returns to without the names it maps to themselves.
func withoutUnchanged(to map[string]string) map[string]string {
	changed := make(map[string]string, len(to))
	for oldName, newName := range to {
		if oldName != newName {
			changed[oldName] = newName
		}
	}

	return changed
}

// renamedRef reports the name to gives the schema s refers to, if s refers to one of its keys.
func renamedRef(s *openapi.Schema, to map[string]string) (string, bool) {
	if s.Ref == nil {
		return "", false
	}

	oldName, ok := strings.CutPrefix(s.Ref.Identifier, schemaRefPrefix)
	if !ok {
		return "", false
	}

	newName, ok := to[oldName]

	return newName, ok
}

// rewriteMappings points every discriminator mapping value that stands for a key of to at that key's value, keeping
// the value's form: a name or a reference.
func rewriteMappings(doc *openapi.Document, to map[string]string) {
	WalkSchemas(doc, func(s *openapi.Schema) {
		if s.Discriminator == nil {
			return
		}

		for key, v := range s.Discriminator.Mapping {
			oldName, ok := strings.CutPrefix(openapi.MappingRef(v.Value), schemaRefPrefix)
			if !ok {
				continue
			}

			newName, ok := to[oldName]
			if !ok {
				continue
			}

			if v.Value == oldName {
				v.Value = newName
			} else {
				v.Value = schemaRefPrefix + newName
			}

			// a copy of the entry keeps its place in the mapping
			s.Discriminator.Mapping[key] = v
		}
	})
}

// implicitMappings finds every discriminator that selects a key of to's schema by its name alone, which renaming or
// redirecting it would break, and returns a function that maps the name to to's value in each.
//
// It looks before references change and adds the entries after mapping values have been rewritten, so neither step
// mistakes the other's names for its own.
//
// Without a mapping entry, a discriminator value names a component schema: one its oneOf or anyOf refers to, or one
// that extends it through allOf.
// See https://spec.openapis.org/oas/v3.1.0#discriminator-object
func implicitMappings(doc *openapi.Document, to map[string]string) (add func()) {
	extending := map[*openapi.Schema][]string{}
	for oldName := range to {
		for _, e := range doc.Components.Schemas[oldName].AllOf {
			if e.Ref != nil && e.Ref.Value != nil {
				extending[e.Ref.Value] = append(extending[e.Ref.Value], oldName)
			}
		}
	}

	implicit := map[*openapi.Discriminator][]string{}

	WalkSchemas(doc, func(s *openapi.Schema) {
		d := s.Discriminator
		if d == nil {
			return
		}

		names := slices.Clone(extending[s])
		for _, e := range slices.Concat(s.OneOf, s.AnyOf) {
			if _, ok := renamedRef(e, to); ok {
				names = append(names, strings.TrimPrefix(e.Ref.Identifier, schemaRefPrefix))
			}
		}

		slices.Sort(names)

		for _, oldName := range slices.Compact(names) {
			if _, mapped := d.Mapping[oldName]; !mapped { // an explicit entry wins over the implicit one
				implicit[d] = append(implicit[d], oldName)
			}
		}
	})

	return func() {
		for d, names := range implicit {
			for _, oldName := range names {
				d.Mapping.Set(oldName, openapi.String{Value: to[oldName]})
			}
		}
	}
}
