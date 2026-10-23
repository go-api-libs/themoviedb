package compress

import (
	"github.com/MarkRosemaker/openapi"
	"github.com/MarkRosemaker/openapi-compare/schema"
)

// schemasSimilarity returns the structural similarity of two schemas in [0, 1].
// Returns 1.0 for same-shape schemas (see schema.SameShape), 0.0 for
// incompatible schemas.
//
// For object schemas with properties the score is a weighted Jaccard index:
//
//	score = Σ weight(p) / |union of property names|
//
// where weight is 1.0 for properties with the same name and same shape,
// and 0.5 for properties with the same name but a different shape.
// All other schema types return 0.0 (they are either the same shape or incompatible).
func schemasSimilarity(a, b *openapi.Schema) float64 {
	if a == b {
		return 1.0
	}

	if a == nil || b == nil {
		return 0.0
	}

	if schema.SameShape(a, b) {
		return 1.0
	}

	if a.Type != b.Type {
		return 0.0
	}

	if a.Type != openapi.TypeObject {
		return 0.0
	}

	union := propertyNameUnion(a.Properties, b.Properties)
	if len(union) == 0 {
		return 0.0
	}

	score := 0.0
	for _, name := range union {
		refA, okA := a.Properties[name]
		refB, okB := b.Properties[name]
		if okA && okB {
			switch {
			case schema.SameShape(refA, refB):
				score += 1.0
			case !reconcilable(refA, refB):
				// No widening covers both, so no amount of agreement
				// elsewhere makes these two schemas mergeable.
				return 0.0
			default:
				score += 0.5 // same name, different shape — partial credit
			}
		}
		// only in one schema → 0 credit
	}

	return score / float64(len(union))
}

// mergeSchemas merges schema b into schema a (modifying a in-place).
// After the call a is a superset of both: it has the union of properties
// (with properties that only exist in b added as optional) and the intersection
// of required fields.  Conflicting inline property schemas are reconciled by
// choosing the more general type (e.g. number over integer).
func mergeSchemas(a, b *openapi.Schema) {
	// Merge properties.
	for name, refB := range b.Properties {
		if refA, ok := a.Properties[name]; ok {
			if !schema.SameShape(refA, refB) {
				a.Properties[name] = reconcileProperty(refA, refB)
			}
		} else {
			// Property only in b — add it to a as optional.
			if a.Properties == nil {
				a.Properties = openapi.Schemas{}
			}

			a.Properties.Set(name, refB)
		}
	}

	intersectRequired(a, b)
}

// intersectRequired keeps a requiring only the fields b requires too. Two schemas of the same shape may still differ
// in this, as the shape leaves out what is required.
func intersectRequired(a, b *openapi.Schema) {
	bRequired := make(map[string]bool, len(b.Required))
	for _, r := range b.Required {
		bRequired[r] = true
	}

	kept := a.Required[:0]
	for _, r := range a.Required {
		if bRequired[r] {
			kept = append(kept, r)
		}
	}

	if len(kept) == 0 {
		kept = nil
	}

	a.Required = kept
}

// reconcileProperty returns the more general of two property schemas.
// Only inline schemas are reconciled; if either side is a $ref, a is kept as-is.
func reconcileProperty(a, b *openapi.Schema) *openapi.Schema {
	if a.Ref != nil || b.Ref != nil {
		return a
	}

	return reconcileInlineSchemas(a, b)
}

// reconcileInlineSchemas returns a copy of a widened to also accept b's values; being a copy, it keeps a's place among the properties.
// Currently handles integer + number → number.
func reconcileInlineSchemas(a, b *openapi.Schema) *openapi.Schema {
	result := *a // shallow copy
	if a.Type == openapi.TypeInteger && b.Type == openapi.TypeNumber {
		result.Type = openapi.TypeNumber
		result.Format = b.Format // adopt number's format (e.g. "double")
	}
	// else if a.Type == openapi.TypeNumber && b.Type == openapi.TypeInteger {
	// 	// a is already the more general type — keep a's format
	// }

	return &result
}

// reconcilable reports whether merging would produce a property that still
// describes both sides.
//
// It almost never does. reconcileProperty keeps a's reference and drops b's,
// and for two inline schemas reconcileInlineSchemas widens integer to number
// and otherwise returns a copy of a. So unless that one widening applies, the
// merged schema quietly claims a shape b's data does not have -- and nothing
// downstream re-checks it against the recording it came from.
//
// Properties present on only one side are a different matter: mergeSchemas
// carries those across as optional, which does describe both.
func reconcilable(a, b *openapi.Schema) bool {
	if a == nil || b == nil {
		return true // nothing to judge it on
	}

	if a.Ref != nil || b.Ref != nil {
		// The references disagree -- schema.SameShape has already said so --
		// and merging keeps a's. If the schemas they point at are themselves
		// mergeable, an earlier or later pass merges them and the references
		// become equal, at which point this pair scores full credit instead.
		return false
	}

	ta, tb := a.Type, b.Type

	return (ta == openapi.TypeInteger && tb == openapi.TypeNumber) ||
		(ta == openapi.TypeNumber && tb == openapi.TypeInteger)
}

// propertyNameUnion returns the union of property names from two property maps.
func propertyNameUnion(a, b openapi.Schemas) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	names := make([]string, 0, len(a)+len(b))
	for name := range a {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}

	for name := range b {
		if _, ok := seen[name]; !ok {
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}

	return names
}

// fillExamples gives a, and the schemas within it, the examples b has where a has none, so merging b away loses none of them.
// A reference is not followed: the schema it points to keeps its own examples.
func fillExamples(a, b *openapi.Schema) {
	if a == nil || b == nil || a.Ref != nil || b.Ref != nil {
		return
	}

	if a.Example == nil {
		a.Example = b.Example
	}

	if len(a.Examples) == 0 {
		a.Examples = b.Examples
	}

	for name, p := range a.Properties {
		fillExamples(p, b.Properties[name])
	}

	fillExamples(a.Items, b.Items)
	fillExamples(a.Not, b.Not)
	fillExamples(a.PropertyNames, b.PropertyNames)

	// schemas of the same shape list the same alternatives and parts, in the same order
	for _, l := range [][2]openapi.SchemaList{{a.PrefixItems, b.PrefixItems}, {a.AllOf, b.AllOf}, {a.OneOf, b.OneOf}, {a.AnyOf, b.AnyOf}} {
		for i, p := range l[0] {
			if i < len(l[1]) {
				fillExamples(p, l[1][i])
			}
		}
	}

	if a.AdditionalProperties != nil && b.AdditionalProperties != nil {
		fillExamples(a.AdditionalProperties.Schema, b.AdditionalProperties.Schema)
	}
}
