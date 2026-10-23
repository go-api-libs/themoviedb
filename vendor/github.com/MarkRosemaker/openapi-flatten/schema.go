package flatten

import (
	"fmt"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
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
		} else if items := s.Items; items != nil { // no items when only ever seen empty
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

// inlineBranches flattens the alternatives of a oneOf or anyOf, naming each by its title, or else by prefix and position.
func inlineBranches(d *openapi.Document, ss openapi.SchemaList, prefix string) error {
	for i, s := range ss {
		name := fmt.Sprintf("%s%d", prefix, i)
		if s.Title != "" {
			name = strcase.ToGoPascal(s.Title)
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
