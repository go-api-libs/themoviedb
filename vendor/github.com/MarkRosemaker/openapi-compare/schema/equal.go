// Package schema provides equality and similarity comparisons for
// github.com/MarkRosemaker/openapi Schema objects.
package schema

import (
	"bytes"
	"encoding/json/jsontext"
	"regexp"
	"slices"

	"github.com/MarkRosemaker/openapi"
)

// Equal reports whether a and b are fully identical, including
// documentation fields (Title, Description, Default, Deprecated, Extensions).
//
// Example and Examples are always ignored: per the OpenAPI/JSON Schema spec
// they are documentation only and never affect what an instance validates against.
func Equal(a, b *openapi.Schema) bool {
	if a == b {
		return true
	}

	if a == nil || b == nil {
		return false
	}

	return a.Title == b.Title &&
		a.Description == b.Description &&
		bytes.Equal(a.Default, b.Default) &&
		a.Deprecated == b.Deprecated &&
		sameCore(a, b, Equal, additionalPropertiesEqual)
}

// SameShape reports whether a and b validate identically: the same JSON
// instances would pass or fail against both schemas.
//
// It ignores documentation-only fields: Title, Description, Default,
// Deprecated, Example and Examples. Extensions and the discriminator are still
// compared, since both can carry meaning that a generic comparison can't rule out.
func SameShape(a, b *openapi.Schema) bool {
	if a == b {
		return true
	}

	if a == nil || b == nil {
		return false
	}

	return sameCore(a, b, SameShape, additionalPropertiesSameShape)
}

// sameCore compares the fields that determine what an instance validates
// against, recursing into nested schemas through match: Equal for a full
// Equal walk, SameShape for a shape-only SameShape walk. apMatch does the
// same for additionalProperties. A $ref is compared by where it points, not
// by following it, which also keeps a self-referential schema from recursing
// forever.
func sameCore(
	a, b *openapi.Schema,
	match func(a, b *openapi.Schema) bool,
	apMatch func(a, b *openapi.AdditionalProperties) bool,
) bool {
	return refsEqual(a.Ref, b.Ref) &&
		a.Type == b.Type &&
		a.Nullable == b.Nullable &&
		a.Format == b.Format &&
		schemaListsMatch(a.AllOf, b.AllOf, match) &&
		schemaListsMatch(a.OneOf, b.OneOf, match) &&
		schemaListsMatch(a.AnyOf, b.AnyOf, match) &&
		match(a.Not, b.Not) &&
		ptrsEqual(a.Min, b.Min) &&
		ptrsEqual(a.Max, b.Max) &&
		ptrsEqual(a.ExclusiveMin, b.ExclusiveMin) &&
		ptrsEqual(a.ExclusiveMax, b.ExclusiveMax) &&
		ptrsEqual(a.MultipleOf, b.MultipleOf) &&
		a.MinLength == b.MinLength &&
		ptrsEqual(a.MaxLength, b.MaxLength) &&
		regexpsEqual(a.Pattern, b.Pattern) &&
		enumsEqual(a.Enum, b.Enum) &&
		bytes.Equal(a.Const, b.Const) &&
		a.MinItems == b.MinItems &&
		ptrsEqual(a.MaxItems, b.MaxItems) &&
		a.UniqueItems == b.UniqueItems &&
		schemaListsMatch(a.PrefixItems, b.PrefixItems, match) &&
		match(a.Items, b.Items) &&
		schemasMatch(a.Properties, b.Properties, match) &&
		slices.Equal(a.Required, b.Required) &&
		apMatch(a.AdditionalProperties, b.AdditionalProperties) &&
		match(a.PropertyNames, b.PropertyNames) &&
		ptrsEqual(a.MaxProperties, b.MaxProperties) &&
		discriminatorsEqual(a.Discriminator, b.Discriminator) &&
		a.ContentMediaType == b.ContentMediaType &&
		a.ContentEncoding == b.ContentEncoding &&
		bytes.Equal(a.Extensions, b.Extensions)
}

// enumsEqual reports whether a and b allow the same values. An absent enum allows any value and an empty one none,
// so the two differ although both hold no values.
func enumsEqual(a, b []jsontext.Value) bool {
	return (a == nil) == (b == nil) &&
		slices.EqualFunc(a, b, func(c, d jsontext.Value) bool { return bytes.Equal(c, d) })
}

// refsEqual reports whether a and b are both absent or point to the same place.
func refsEqual(a, b *openapi.SchemaRef) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.Identifier == b.Identifier
}

func discriminatorsEqual(a, b *openapi.Discriminator) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.PropertyName == b.PropertyName &&
		mappingsEqual(a.Mapping, b.Mapping) &&
		bytes.Equal(a.Extensions, b.Extensions)
}

// mappingsEqual reports whether a and b map the same values to the same schemas, whether a schema is named or referenced.
func mappingsEqual(a, b openapi.MapOfStrings) bool {
	if len(a) != len(b) {
		return false
	}

	for key, av := range a {
		bv, ok := b[key]
		if !ok || openapi.MappingRef(av.Value) != openapi.MappingRef(bv.Value) {
			return false
		}
	}

	return true
}

// additionalPropertiesEqual reports whether a and b are written identically:
// both absent, the same boolean, or fully identical schemas.
func additionalPropertiesEqual(a, b *openapi.AdditionalProperties) bool {
	return additionalPropertiesMatch(a, b, Equal)
}

// additionalPropertiesSameShape reports whether a and b accept the same extra
// properties. Absent, true and the empty schema all accept any.
func additionalPropertiesSameShape(a, b *openapi.AdditionalProperties) bool {
	if acceptsAny(a) && acceptsAny(b) {
		return true
	}

	return additionalPropertiesMatch(a, b, SameShape)
}

func additionalPropertiesMatch(a, b *openapi.AdditionalProperties, match func(a, b *openapi.Schema) bool) bool {
	if a == nil || b == nil {
		return a == b
	}

	if a.Schema != nil || b.Schema != nil {
		return match(a.Schema, b.Schema)
	}

	return a.Allowed == b.Allowed
}

func acceptsAny(ap *openapi.AdditionalProperties) bool {
	switch {
	case ap == nil:
		return true
	case ap.Schema == nil:
		return ap.Allowed
	default:
		return SameShape(ap.Schema, &openapi.Schema{})
	}
}

func schemaListsMatch(a, b openapi.SchemaList, match func(a, b *openapi.Schema) bool) bool {
	return slices.EqualFunc(a, b, match)
}

func schemasMatch(a, b openapi.Schemas, match func(a, b *openapi.Schema) bool) bool {
	if len(a) != len(b) {
		return false
	}

	for k, va := range a {
		vb, ok := b[k]
		if !ok || !match(va, vb) {
			return false
		}
	}

	return true
}

func ptrsEqual[T comparable](a, b *T) bool {
	if a == b {
		return true
	}

	if a == nil || b == nil {
		return false
	}

	return *a == *b
}

func regexpsEqual(a, b *regexp.Regexp) bool {
	if a == b {
		return true
	}

	if a == nil || b == nil {
		return false
	}

	return a.String() == b.String()
}
