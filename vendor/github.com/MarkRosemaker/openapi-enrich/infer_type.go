package enrich

import (
	"encoding/json/jsontext"
	"math"
	"strconv"

	"github.com/MarkRosemaker/openapi"
	edit "github.com/MarkRosemaker/openapi-edit"
)

// inferTypes gives every schema in doc without a type the one its enum and const values imply.
func inferTypes(doc *openapi.Document) {
	edit.WalkSchemas(doc, inferType)
}

// inferType sets the type of s from its enum and const values, if it has none and they are of a single kind.
func inferType(s *openapi.Schema) {
	if s.Type != "" {
		return
	}

	values := s.Enum
	if len(s.Const) > 0 {
		values = append(values[:len(values):len(values)], s.Const)
	}

	var (
		typ      openapi.DataType
		nullable bool
	)

	for _, v := range values {
		t := typeOf(v)
		switch {
		case t == openapi.TypeNull:
			nullable = true
		case typ == "" || typ == t:
			typ = t
		case typ == openapi.TypeInteger && t == openapi.TypeNumber,
			typ == openapi.TypeNumber && t == openapi.TypeInteger:
			typ = openapi.TypeNumber
		default:
			return // values of several kinds
		}
	}

	switch {
	case typ != "":
		s.Type, s.Nullable = typ, nullable
	case nullable:
		s.Type = openapi.TypeNull
	}
}

// typeOf returns the JSON Schema type of v, which is integer for any number without a fractional part.
func typeOf(v jsontext.Value) openapi.DataType {
	switch v.Kind() {
	case '"':
		return openapi.TypeString
	case 't', 'f':
		return openapi.TypeBoolean
	case 'n':
		return openapi.TypeNull
	case '[':
		return openapi.TypeArray
	case '{':
		return openapi.TypeObject
	case '0':
		if f, err := strconv.ParseFloat(string(v), 64); err == nil && f == math.Trunc(f) {
			return openapi.TypeInteger
		}

		return openapi.TypeNumber
	default:
		return ""
	}
}
