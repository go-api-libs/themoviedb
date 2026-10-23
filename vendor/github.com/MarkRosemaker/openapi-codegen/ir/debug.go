package ir

import "github.com/MarkRosemaker/openapi"

// narrowUnspecified makes every value doc leaves open an object of no members: the empty schema, an array's missing
// items and a free-form object's values. Each then decodes into struct{} instead of any, so that a client generated
// in debug mode fails on whatever the specification does not describe yet, and records it for enrich to fill in.
//
// A union's alternatives are left as they are: what is open there is the union, not the specification.
func narrowUnspecified(doc *openapi.Document) {
	n := narrower{}

	for _, s := range doc.Components.Schemas {
		n.value(s)
	}

	for _, r := range doc.Components.Responses {
		if r != nil && r.Value != nil {
			n.content(r.Value.Content)
		}
	}

	for _, rb := range doc.Components.RequestBodies {
		if rb != nil && rb.Value != nil {
			n.content(rb.Value.Content)
		}
	}

	for _, p := range doc.Paths {
		for _, op := range p.Operations {
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				n.content(op.RequestBody.Value.Content)
			}

			for _, r := range op.Responses {
				if r != nil && r.Value != nil {
					n.content(r.Value.Content)
				}
			}
		}
	}
}

// narrower walks the schemas of a document for narrowUnspecified, each once.
type narrower map[*openapi.Schema]bool

// content narrows the schema of each media type in c.
func (n narrower) content(c openapi.Content) {
	for _, mt := range c {
		if mt != nil {
			n.value(mt.Schema)
		}
	}
}

// value narrows s and walks into it, for a schema that stands for a value of its own.
func (n narrower) value(s *openapi.Schema) {
	if s == nil {
		return
	}

	switch {
	case s.Ref != nil:
	case isOpen(s) || isOpenBut(s):
		s.Type = openapi.TypeObject
	case s.Type == openapi.TypeArray && s.Items == nil:
		s.Items = &openapi.Schema{Type: openapi.TypeObject}
	case len(s.Properties) == 0 && s.AdditionalProperties != nil &&
		(s.AdditionalProperties.Schema == nil && s.AdditionalProperties.Allowed || isOpen(s.AdditionalProperties.Schema)):
		s.AdditionalProperties = nil
	}

	n.walk(s)
}

// walk narrows the values s holds, and walks into the schemas it is made of.
func (n narrower) walk(s *openapi.Schema) {
	if s == nil || n[s] {
		return
	}

	n[s] = true

	if s.Ref != nil {
		n.walk(s.Ref.Value)
		return
	}

	for _, p := range s.Properties {
		n.value(p)
	}

	n.value(s.Items)

	for _, p := range s.PrefixItems {
		n.value(p)
	}

	if s.AdditionalProperties != nil {
		n.value(s.AdditionalProperties.Schema)
	}

	// X in "X or null" stands for the value
	if v := nullableVariant(s); v != nil {
		n.value(v)
	}

	for _, l := range []openapi.SchemaList{s.AllOf, s.OneOf, s.AnyOf} {
		for _, e := range l {
			n.walk(e)
		}
	}
}

// isOpen reports whether s is the empty schema, which any value matches.
func isOpen(s *openapi.Schema) bool {
	return s != nil && s.Ref == nil && s.Type == "" && len(s.Properties) == 0 && s.AdditionalProperties == nil &&
		len(s.AllOf) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 && s.Not == nil && len(s.Enum) == 0 &&
		len(s.Const) == 0 && s.Items == nil && len(s.PrefixItems) == 0
}

// isOpenBut reports whether s is the empty schema but for a not, such as "not": {}, which no value matches.
func isOpenBut(s *openapi.Schema) bool {
	if s == nil || s.Not == nil {
		return false
	}

	c := *s
	c.Not = nil

	return isOpen(&c)
}
