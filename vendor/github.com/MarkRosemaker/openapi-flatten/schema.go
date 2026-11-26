package flatten

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"strings"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// inlineSchema moves s into the components if it deserves a name of its own, leaving a reference in its place, and flattens what it contains.
func inlineSchema(d *openapi.Document, s *openapi.Schema, name string, alwaysMove bool) error {
	if s.Ref != nil {
		return nil // already processed
	}

	if alwaysMove {
		// process the schema itself
		return schema(d, moveSchemaToComponents(d, name, s), name)
	}

	switch s.Type {
	case openapi.TypeInteger, openapi.TypeNumber, openapi.TypeBoolean, openapi.TypeNull: // no need to move to components
	case openapi.TypeString:
		if s.Enum != nil {
			s = moveSchemaToComponents(d, name, s)
		} // else just string, no need to move to components
	case openapi.TypeArray:
		if len(s.PrefixItems) > 0 {
			// a tuple has a defined shape, positional but no less real than
			// an object's properties: give it a name of its own the same way.
			s = moveSchemaToComponents(d, name, s)
		} else if items := s.Items; items != nil && items.Ref != nil {
			// a list of a component is named after it, so the component's list reads the same wherever it is
			if item, ok := strings.CutPrefix(items.Ref.Identifier, "#/components/schemas/"); ok {
				s = moveListToComponents(d, s, item)
			}
		} else if items != nil { // no items when only ever seen empty
			switch items.Type {
			case openapi.TypeInteger: // do nothing, just []int
			case openapi.TypeNumber: // do nothing, just []float32 or []float64
			case openapi.TypeString:
				if items.Enum != nil {
					s = moveSchemaToComponents(d, name, s)
				} // else just []string, no need to move to components
			case openapi.TypeObject:
				if len(items.Properties) > 0 {
					s = moveSchemaToComponents(d, name, s)
				}
			case openapi.TypeBoolean: // do nothing, just []bool
			case openapi.TypeNull: // do nothing, just []null
			case openapi.TypeArray: // TODO: later
			case "": // no explicit type — items uses anyOf / oneOf / allOf (e.g. nullable union)
			default:
				return fmt.Errorf("unimplemented item type %q", items.Type)
			}
		}
	case openapi.TypeObject: // move to components
		if len(s.Properties) > 0 {
			s = moveSchemaToComponents(d, name, s)
		}
	case "": // no explicit type — oneOf / anyOf / allOf composition or bare properties
		if alt := nonNull(s); alt != nil {
			inline := alt.Ref == nil

			// X or null is X that may be null, not a shape of its own
			if err := inlineSchema(d, alt, name, false); err != nil {
				return err
			}

			// what moved says what X is, as it would have if X had been written alone
			if moved := alt.Ref; inline && moved != nil && moved.Value.Description == "" {
				moved.Value.Description = s.Description
			}

			return nil
		}

		v := s
		hasComposition := len(v.OneOf) > 0 || len(v.AnyOf) > 0 || len(v.AllOf) > 0
		if hasComposition || len(v.Properties) > 0 {
			s = moveSchemaToComponents(d, name, s)
		}
	default:
		return fmt.Errorf("unimplemented schema ref type %q", s.Type)
	}

	// process the schema itself
	return schema(d, s, name)
}

func schema(d *openapi.Document, s *openapi.Schema, name string) error {
	switch s.Type {
	case openapi.TypeString,
		openapi.TypeInteger,
		openapi.TypeNumber,
		openapi.TypeBoolean,
		openapi.TypeNull: // no need to do anything
		return nil
	case openapi.TypeArray, openapi.TypeObject: // do below
	case "": // is valid if schema contains oneOf, anyOf, allOf, or properties
	default:
		return fmt.Errorf("unimplemented schema type %q", s.Type)
	}

	// each entry with a shape of its own is named like any other schema
	if err := inlineSchemaList(d, s.AllOf, name+"AllOf", false); err != nil {
		return &errpath.ErrField{Field: "allOf", Err: err}
	}

	if err := inlineBranches(d, s.OneOf, name+"OneOf"); err != nil {
		return &errpath.ErrField{Field: "oneOf", Err: err}
	}

	if err := inlineBranches(d, s.AnyOf, name+"AnyOf"); err != nil {
		return &errpath.ErrField{Field: "anyOf", Err: err}
	}

	// each position is a real, reusable shape, the same as an object property
	// just addressed by index instead of by name.
	if err := inlineSchemaList(d, s.PrefixItems, name+"Item", false); err != nil {
		return &errpath.ErrField{Field: "prefixItems", Err: err}
	}

	if s.Items != nil {
		if err := inlineSchema(d, s.Items, name+"Item", false); err != nil {
			return &errpath.ErrField{Field: "items", Err: err}
		}
	}

	if err := inlineSchemas(d, s.Properties, name); err != nil {
		return &errpath.ErrField{Field: "properties", Err: err}
	}

	if ap := s.AdditionalProperties; ap != nil && ap.Schema != nil {
		if err := inlineSchema(d, ap.Schema, name+"Value", false); err != nil {
			return &errpath.ErrField{Field: "additionalProperties", Err: err}
		}
	}

	if s.PropertyNames != nil {
		if err := inlineSchema(d, s.PropertyNames, name+"Key", false); err != nil {
			return &errpath.ErrField{Field: "propertyNames", Err: err}
		}
	}

	if s.Not != nil {
		if err := inlineSchema(d, s.Not, name+"Not", false); err != nil {
			return &errpath.ErrField{Field: "not", Err: err}
		}
	}

	return nil
}

// moveSchemaToComponents puts a copy of s in the components and makes s a reference to it, keeping s's place. It returns the copy.
func moveSchemaToComponents(d *openapi.Document, name string, s *openapi.Schema) *openapi.Schema {
	moved := *s
	name = uniqueName(d.Components.Schemas, name)
	d.Components.Schemas.Set(name, &moved)
	s.Replace(&openapi.Schema{Ref: &openapi.SchemaRef{Identifier: newRef("schemas", name).Identifier, Value: &moved}})

	return &moved
}

// moveListToComponents makes s, a list of the component schema item, a reference to the component listing it, which is
// named the plural of item: an existing one, if it lists item and nothing else, or else a new one. It returns the
// component.
func moveListToComponents(d *openapi.Document, s *openapi.Schema, item string) *openapi.Schema {
	name := plural(item)

	if existing, ok := d.Components.Schemas[name]; ok && isListOf(existing, newRef("schemas", item).Identifier) {
		s.Replace(&openapi.Schema{
			Ref:         &openapi.SchemaRef{Identifier: newRef("schemas", name).Identifier, Value: existing},
			Description: s.Description,
		})

		return existing
	}

	return moveSchemaToComponents(d, name, s)
}

// isListOf reports whether s is a list of the component schema at ref and nothing else, but for what it says of itself:
// its title, description, examples and extensions.
func isListOf(s *openapi.Schema, ref string) bool {
	if s.Type != openapi.TypeArray || s.Items == nil || s.Items.Ref == nil || s.Items.Ref.Identifier != ref {
		return false
	}

	rest := *s
	rest.Title, rest.Description, rest.Example, rest.Examples, rest.Extensions = "", "", nil, nil, nil

	got, err := json.Marshal(&rest)
	if err != nil {
		return false
	}

	want, err := json.Marshal(&openapi.Schema{Type: s.Type, Items: s.Items})

	return err == nil && bytes.Equal(got, want)
}

// plural is the English plural of a name, such as Orders of Order, Entries of Entry and Statuses of Status.
func plural(name string) string {
	switch {
	case strings.HasSuffix(name, "s"), strings.HasSuffix(name, "x"), strings.HasSuffix(name, "z"),
		strings.HasSuffix(name, "ch"), strings.HasSuffix(name, "sh"):
		return name + "es"
	case strings.HasSuffix(name, "y") && len(name) > 1 && !strings.ContainsRune("aeiouAEIOU", rune(name[len(name)-2])):
		return name[:len(name)-1] + "ies"
	default:
		return name + "s"
	}
}

// inlineBranches flattens the alternatives of a oneOf or anyOf, naming each by its title, or else by prefix and position.
func inlineBranches(d *openapi.Document, ss openapi.SchemaList, prefix string) error {
	for i, s := range ss {
		name := fmt.Sprintf("%s%d", prefix, i)
		if s.Title != "" {
			name = goName(s.Title)
		}

		if err := inlineSchema(d, s, name, false); err != nil {
			return &errpath.ErrIndex{Index: i, Err: err}
		}
	}

	return nil
}

func inlineSchemaList(d *openapi.Document, ss openapi.SchemaList, prefix string, alwaysMove bool) error {
	for i, s := range ss {
		if err := inlineSchema(d, s, fmt.Sprintf("%s%d", prefix, i), alwaysMove); err != nil {
			return &errpath.ErrIndex{Index: i, Err: err}
		}
	}

	return nil
}

// nonNull is the alternative to null of a oneOf or anyOf of two, or nil where s is no such schema.
func nonNull(s *openapi.Schema) *openapi.Schema {
	alts := s.AnyOf
	if len(alts) == 0 {
		alts = s.OneOf
	}

	switch {
	case len(alts) != 2 || len(s.AnyOf) > 0 && len(s.OneOf) > 0 || len(s.AllOf) > 0 || len(s.Properties) > 0:
		return nil
	case alts[1].Type == openapi.TypeNull && alts[0].Type != openapi.TypeNull:
		return alts[0]
	case alts[0].Type == openapi.TypeNull && alts[1].Type != openapi.TypeNull:
		return alts[1]
	default:
		return nil
	}
}
