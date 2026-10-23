package ir

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// zeroIsAValue reports whether the zero value of tp, the Go type of s, is a value s can hold: one that sending says
// something leaving the field out does not. Only then does a field need a pointer, nil to leave it out and a pointer to
// the zero value to send it.
//
// A string, a time or date, a slice, a map or a union never needs one: an empty string is no value, and nil or an unset union
// already says nothing is there.
func zeroIsAValue(s *openapi.Schema, tp *GoType) bool {
	if tp.IsSlice || tp.IsNilable || tp.IsArrayOfSize > 0 || tp.Name == "any" || tp.Name == "time.Time" || tp.Name == "civil.Date" ||
		strings.HasPrefix(tp.Name, "map[") {
		return false
	}

	s = deref(s)
	if v := nullableVariant(s); v != nil {
		s = deref(v)
	}

	if s == nil || len(s.Default) > 0 && isZeroJSON(s.Default) { // leaving it out already means the zero value
		return false
	}

	switch s.Type {
	case openapi.TypeBoolean, openapi.TypeInteger, openapi.TypeNumber:
		switch {
		case len(s.Const) > 0:
			return isZeroJSON(s.Const)
		case len(s.Enum) > 0:
			return slices.ContainsFunc(s.Enum, isZeroJSON)
		default:
			return zeroInRange(s)
		}
	case openapi.TypeObject:
		return !hasRequired(s) // {} is a value of an object that requires nothing
	case "":
		return len(s.AllOf) > 0 && !isUnion(s) && !hasRequired(s)
	default:
		return false
	}
}

// isZeroJSON reports whether v is false, 0 or {}.
func isZeroJSON(v jsontext.Value) bool {
	var x any
	if err := json.Unmarshal(v, &x); err != nil {
		return false
	}

	switch x := x.(type) {
	case bool:
		return !x
	case float64:
		return x == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

// zeroInRange reports whether s's bounds allow 0.
func zeroInRange(s *openapi.Schema) bool {
	return (s.Min == nil || *s.Min <= 0) && (s.Max == nil || *s.Max >= 0) &&
		(s.ExclusiveMin == nil || *s.ExclusiveMin < 0) && (s.ExclusiveMax == nil || *s.ExclusiveMax > 0)
}

// hasRequired reports whether s, or a schema it extends through allOf, requires a property.
func hasRequired(s *openapi.Schema) bool {
	return len(requiredOf(s)) > 0 || slices.ContainsFunc(s.AllOf, func(p *openapi.Schema) bool {
		p = deref(p)
		return p != nil && hasRequired(p)
	})
}

// pointRecursiveFields gives a pointer to each field that makes a struct contain itself, which Go cannot lay out.
//
// Fields are visited in order, and one becomes a pointer only if its type still leads back to its struct through the
// fields that are not pointers yet, so each cycle gets as few pointers as this order allows.
func pointRecursiveFields(schemas []Schema) {
	structs := make(map[string]*Schema, len(schemas))
	for i := range schemas {
		if k := schemas[i].Kind; k == SchemaKindStruct || k == SchemaKindAllOf {
			structs[schemas[i].Name] = &schemas[i]
		}
	}

	// reaches reports whether the struct named from contains the one named to without a pointer between them
	reaches := func(from, to string) bool {
		seen := map[string]bool{}

		var walk func(name string) bool

		walk = func(name string) bool {
			if name == to {
				return true
			}

			if seen[name] {
				return false
			}

			seen[name] = true

			s := structs[name]
			if s == nil {
				return false
			}

			return slices.ContainsFunc(s.Fields, func(f Field) bool { return walk(f.Type) })
		}

		return walk(from)
	}

	for _, s := range schemas {
		if structs[s.Name] == nil {
			continue
		}

		for i, f := range s.Fields {
			if !f.Embedded && structs[f.Type] != nil && reaches(f.Type, s.Name) {
				s.Fields[i].Type = "*" + f.Type
			}
		}
	}
}
