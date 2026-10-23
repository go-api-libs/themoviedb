package flatten

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strconv"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// ExtensionOrigin is the extension on a component schema Document moved out of the document, given [Config.MarkOrigin]:
// a JSON pointer to the reference left in its place, such as
// "#/components/schemas/Page/properties/cover". A schema without it was a component already.
const ExtensionOrigin = "x-flattened-from"

// escape escapes a key for a JSON pointer.
var escape = strings.NewReplacer("~", "~0", "/", "~1")

// markOrigins sets [ExtensionOrigin] on each component schema in created to where the reference to it is.
func markOrigins(d *openapi.Document, created map[string]bool) {
	m := &originMarker{created: created, marked: map[string]bool{}}

	for name, s := range d.Components.Schemas.ByIndex() {
		m.schema(s, "#/components/schemas/"+escape.Replace(name))
	}

	for name, r := range d.Components.Responses.ByIndex() {
		m.response(r, "#/components/responses/"+escape.Replace(name))
	}

	for name, rb := range d.Components.RequestBodies.ByIndex() {
		m.requestBody(rb, "#/components/requestBodies/"+escape.Replace(name))
	}

	for name, p := range d.Components.Parameters.ByIndex() {
		m.parameter(p, "#/components/parameters/"+escape.Replace(name))
	}

	for path, pi := range d.Paths.ByIndex() {
		at := "#/paths/" + escape.Replace(string(path))

		for i, p := range pi.Parameters {
			m.parameter(p, at+"/parameters/"+strconv.Itoa(i))
		}

		for method, op := range pi.Operations {
			m.operation(op, at+"/"+strings.ToLower(method))
		}
	}
}

// originMarker walks a document, knowing where it is, and marks each created schema where it meets its reference.
type originMarker struct {
	created, marked map[string]bool
}

func (m *originMarker) operation(op *openapi.Operation, at string) {
	for i, p := range op.Parameters {
		m.parameter(p, at+"/parameters/"+strconv.Itoa(i))
	}

	m.requestBody(op.RequestBody, at+"/requestBody")

	for code, r := range op.Responses.ByIndex() {
		m.response(r, at+"/responses/"+escape.Replace(string(code)))
	}
}

func (m *originMarker) parameter(p *openapi.ParameterRef, at string) {
	if p != nil && p.Ref == nil && p.Value != nil {
		m.schema(p.Value.Schema, at+"/schema")
		m.content(p.Value.Content, at)
	}
}

func (m *originMarker) response(r *openapi.ResponseRef, at string) {
	if r != nil && r.Ref == nil && r.Value != nil {
		m.content(r.Value.Content, at)
	}
}

func (m *originMarker) requestBody(rb *openapi.RequestBodyRef, at string) {
	if rb != nil && rb.Ref == nil && rb.Value != nil {
		m.content(rb.Value.Content, at)
	}
}

func (m *originMarker) content(c openapi.Content, at string) {
	for mr, mt := range c.ByIndex() {
		if mt != nil {
			m.schema(mt.Schema, at+"/content/"+escape.Replace(string(mr))+"/schema")
		}
	}
}

func (m *originMarker) schema(s *openapi.Schema, at string) {
	if s == nil {
		return
	}

	if s.Ref != nil {
		// flatten leaves one reference to each schema it moves; it is where the schema came from
		if name, ok := strings.CutPrefix(s.Ref.Identifier, "#/components/schemas/"); ok && m.created[name] && !m.marked[name] {
			m.marked[name] = true

			setExtension(s.Ref.Value, ExtensionOrigin, at)
		}

		return
	}

	for _, l := range []struct {
		field string
		list  openapi.SchemaList
	}{{"allOf", s.AllOf}, {"oneOf", s.OneOf}, {"anyOf", s.AnyOf}, {"prefixItems", s.PrefixItems}} {
		for i, e := range l.list {
			m.schema(e, at+"/"+l.field+"/"+strconv.Itoa(i))
		}
	}

	m.schema(s.Not, at+"/not")
	m.schema(s.Items, at+"/items")

	if s.AdditionalProperties != nil {
		m.schema(s.AdditionalProperties.Schema, at+"/additionalProperties")
	}

	m.schema(s.PropertyNames, at+"/propertyNames")

	for name, p := range s.Properties.ByIndex() {
		m.schema(p, at+"/properties/"+escape.Replace(name))
	}
}

// setExtension sets the extension name of s to the string value, keeping its others.
func setExtension(s *openapi.Schema, name, value string) {
	ext := map[string]jsontext.Value{}
	if len(s.Extensions) > 0 {
		if err := json.Unmarshal(s.Extensions, &ext); err != nil {
			return
		}
	}

	v, err := json.Marshal(value)
	if err != nil {
		return
	}

	ext[name] = v

	if b, err := json.Marshal(ext, json.Deterministic(true)); err == nil {
		s.Extensions = b
	}
}
