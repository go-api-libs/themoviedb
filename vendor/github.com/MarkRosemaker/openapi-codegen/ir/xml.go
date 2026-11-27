package ir

import (
	"fmt"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// markXMLTags gives the fields of every schema an XML response holds, there or deeper, the xml tag that reads them as
// the schema's xml objects say: by their element's or attribute's name, a wrapped list through its wrapper.
func markXMLTags(doc *openapi.Document, schemas []Schema) {
	inXML := map[string]bool{}

	var visit func(s *openapi.Schema)

	visit = func(s *openapi.Schema) {
		if s == nil {
			return
		}

		if s.Ref != nil {
			name, ok := strings.CutPrefix(s.Ref.Identifier, "#/components/schemas/")
			if !ok || inXML[name] {
				return
			}

			inXML[name] = true
		}

		v := deref(s)
		for _, p := range v.Properties.ByIndex() {
			visit(p)
		}

		visit(v.Items)

		for _, l := range []openapi.SchemaList{v.AllOf, v.OneOf, v.AnyOf} {
			for _, alt := range l {
				visit(alt)
			}
		}
	}

	for _, path := range doc.Paths.ByIndex() {
		for _, op := range path.Operations {
			for _, r := range op.Responses.ByIndex() {
				for mr, mt := range r.Value.Content.ByIndex() {
					if isXMLMediaType(string(mr)) {
						visit(mt.Schema)
					}
				}
			}
		}
	}

	for i := range schemas {
		s := &schemas[i]
		if !inXML[s.Name] {
			continue
		}

		comp := doc.Components.Schemas[s.Name]
		if comp == nil {
			continue
		}

		for j := range s.Fields {
			f := &s.Fields[j]
			if prop := comp.Properties[f.JSONName]; prop != nil {
				f.JSONTag += " " + xmlTag(f.JSONName, prop, f.Required)
			}
		}
	}
}

// xmlTag is the xml struct tag of the property name holding prop: an attribute, an element, or a wrapped list's items
// inside their wrapper. An optional one is left out when it holds nothing.
func xmlTag(name string, prop *openapi.Schema, required bool) string {
	v := deref(prop)

	x := prop.XML
	if x == nil {
		x = v.XML
	}

	elem := name
	if x != nil && x.Name != "" {
		elem = x.Name
	}

	opt := ""
	if !required {
		opt = ",omitempty"
	}

	switch {
	case x != nil && x.Attribute:
		return fmt.Sprintf(`xml:"%s,attr%s"`, elem, opt)
	case v.Type == openapi.TypeArray && v.Items != nil:
		item := elem
		if ix := deref(v.Items).XML; ix != nil && ix.Name != "" {
			item = ix.Name
		} else if v.Items.XML != nil && v.Items.XML.Name != "" {
			item = v.Items.XML.Name
		}

		if x != nil && x.Wrapped {
			return fmt.Sprintf(`xml:"%s>%s%s"`, elem, item, opt)
		}

		// the items stand side by side, each an element of the items' name
		return fmt.Sprintf(`xml:"%s%s"`, item, opt)
	default:
		return fmt.Sprintf(`xml:"%s%s"`, elem, opt)
	}
}
