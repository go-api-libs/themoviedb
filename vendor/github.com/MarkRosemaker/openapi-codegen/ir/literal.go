package ir

import (
	"slices"
	"strconv"
	"strings"
)

// MinimalLiteral is a Go literal of goType that marshals: every union in it that a value
// requires, itself included, has its first variant set.
func (doc Document) MinimalLiteral(goType string) string {
	lit, _ := doc.minimalLiteral(goType, map[string]bool{})
	return lit
}

// minimalLiteral also reports whether the literal sets anything.
func (doc Document) minimalLiteral(goType string, seen map[string]bool) (string, bool) {
	zero := goType + "{}"

	if seen[goType] {
		return zero, false
	}

	var s *Schema
	for j := range doc.Schemas {
		if doc.Schemas[j].Name == goType {
			s = &doc.Schemas[j]
			break
		}
	}

	if s == nil {
		return zero, false
	}

	seen[goType] = true
	defer delete(seen, goType)

	var parts []string

	switch s.Kind {
	case SchemaKindUnion:
		if len(s.UnionVariants) == 0 {
			return zero, false
		}

		v := s.UnionVariants[0]
		parts = append(parts, v.FieldName+": "+doc.setLiteral(v.Type, v.Zero, seen))
	case SchemaKindStruct, SchemaKindAllOf:
		for _, f := range s.Fields {
			if strings.HasPrefix(f.Type, "*") || !f.Required && !f.Embedded {
				continue
			}

			if lit, set := doc.minimalLiteral(f.Type, seen); set {
				name := f.Name
				if f.Embedded {
					name = f.Type
				}

				parts = append(parts, name+": "+lit)
			}
		}

		if t := s.Tagged; t != nil {
			v := t.Values[0]
			parts = append(parts, t.Field+": "+strconv.Quote(v.Value))

			for _, m := range t.Members {
				if !slices.Contains(v.Members, TaggedOwn{Name: m.Name, Required: true}) { // null cannot be told from unset
					continue
				}

				i := slices.IndexFunc(s.Fields, func(f Field) bool { return f.Name == m.Field })
				parts = append(parts, m.Field+": "+doc.setLiteral(strings.TrimPrefix(s.Fields[i].Type, "*"), m.Zero, seen))
			}
		}
	default:
	}

	if len(parts) == 0 {
		return zero, false
	}

	return goType + "{" + strings.Join(parts, ", ") + "}", true
}

// setLiteral is a Go literal of a field of type goType, or of a pointer to it if zero, its unset value, is "nil" and
// goType itself cannot be nil, that reads as set: any value but zero.
func (doc Document) setLiteral(goType, zero string, seen map[string]bool) string {
	switch {
	case zero == "" || zero == "nil" && !nilable(goType):
		if l, set := doc.minimalLiteral(goType, seen); set {
			return "&" + l
		}

		return "new(" + goType + ")"
	case zero == `""`:
		return `"-"` // any but the empty string, which reads as not set
	case goType == "any":
		return "struct{}{}"
	default:
		return goType + "{}" // empty, but not nil
	}
}

// nilable reports whether a value of goType can be nil.
func nilable(goType string) bool {
	return goType == "any" || strings.HasPrefix(goType, "[]") || strings.HasPrefix(goType, "map[")
}
