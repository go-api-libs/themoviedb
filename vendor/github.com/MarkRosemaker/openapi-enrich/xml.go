package enrich

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi"
)

// isXMLMediaType reports whether mediaType is XML: text/xml, application/xml or one ending in +xml.
func isXMLMediaType(mediaType string) bool {
	return mediaType == "text/xml" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml")
}

// element is an XML element as read: its attributes, its child elements in order, and its text.
type element struct {
	name     string
	attrs    []xml.Attr
	children []*element
	text     strings.Builder
}

// newSchemaFromXML infers a schema from an XML document: an object named after its root element, whose attributes and
// child elements are its properties.
func newSchemaFromXML(data []byte) (*openapi.Schema, error) {
	root, err := readXML(data)
	if err != nil {
		return nil, err
	}

	s := elementSchema(root)
	if s.Type != openapi.TypeObject {
		// a root holding only text is still an element of its own
		s = &openapi.Schema{Type: openapi.TypeObject}
	}

	s.XML = &openapi.XML{Name: root.name}

	return s, nil
}

// readXML reads the root element of data and everything in it.
func readXML(data []byte) (*element, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))

	var stack []*element

	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no root element")
		}

		if err != nil {
			return nil, err
		}

		switch t := tok.(type) {
		case xml.StartElement:
			e := &element{name: t.Name.Local, attrs: t.Attr}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, e)
			}

			stack = append(stack, e)
		case xml.EndElement:
			e := stack[len(stack)-1]
			if stack = stack[:len(stack)-1]; len(stack) == 0 {
				return e, nil
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		}
	}
}

// elementSchema infers the schema of e. Its text is a string, as every value in XML is: a code such as 0123 is not a
// number. An element with attributes or child elements is an object of them, and a child element that repeats is a
// list.
func elementSchema(e *element) *openapi.Schema {
	if len(e.attrs) == 0 && len(e.children) == 0 {
		return xmlString(e.text.String())
	}

	s := &openapi.Schema{Type: openapi.TypeObject, Properties: openapi.Schemas{}}

	for _, a := range e.attrs {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue // a namespace declaration, not a value
		}

		attr := xmlString(a.Value)
		attr.XML = &openapi.XML{Attribute: true}
		s.Properties.Set(a.Name.Local, attr)
		s.Required = append(s.Required, a.Name.Local)
	}

	var (
		order []string
		byKey = map[string][]*openapi.Schema{}
	)

	for _, c := range e.children {
		if _, ok := byKey[c.name]; !ok {
			order = append(order, c.name)
		}

		byKey[c.name] = append(byKey[c.name], elementSchema(c))
	}

	for _, name := range order {
		schemas := byKey[name]

		prop := schemas[0]
		if len(schemas) > 1 {
			prop = &openapi.Schema{Type: openapi.TypeArray}
			if item, ok := mergeHomogeneous(schemas); ok {
				prop.Items = item
			} else {
				prop.Items = schemas[0]
			}
		}

		s.Properties.Set(name, prop)
		s.Required = append(s.Required, name)
	}

	return s
}

// xmlString is the schema of an XML value: a string, with the value as its example unless it is masked.
func xmlString(value string) *openapi.Schema {
	value = strings.TrimSpace(value)

	s := &openapi.Schema{Type: openapi.TypeString}
	if value != "" && !cassette.IsMasked(value) {
		s.Example = jsontext.Value(fmt.Appendf(nil, "%q", value))
	}

	return s
}
