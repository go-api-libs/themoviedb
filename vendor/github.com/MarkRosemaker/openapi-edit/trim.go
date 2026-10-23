package edit

import (
	"bytes"
	"encoding/json/jsontext"
	"fmt"
	"slices"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// DefaultMaxArrayExamples is the array length [TrimExample] and
// [TrimSchemaExamples] use when the caller passes 0 for maxItems.
const DefaultMaxArrayExamples = 3

// TrimExample returns v with every array it contains, at any depth, cut
// down to at most maxItems elements. Object key order and every kept
// value's own bytes are preserved exactly; only arrays are shortened. If
// maxItems is 0, [DefaultMaxArrayExamples] is used.
//
// Elements are chosen to be representative rather than just the first few:
// an array keeps one element per distinct shape it observes first — an
// object's own set of keys, or a scalar's JSON type — in the order they
// appear, before repeating one to fill the rest of the budget. A field only
// some elements carry, or a value that is sometimes a number and sometimes
// a string, therefore survives trimming instead of being cut away by
// chance.
//
// v must be well-formed JSON; TrimExample returns an error otherwise.
func TrimExample(v jsontext.Value, maxItems int) (jsontext.Value, error) {
	if maxItems <= 0 {
		maxItems = DefaultMaxArrayExamples
	}

	return trimJSON(v, maxItems)
}

// trimJSON does the actual work behind [TrimExample], on an already
// resolved maxItems (a nested call never repeats the maxItems<=0 check,
// since it will already have been resolved at the top by TrimExample).
func trimJSON(v jsontext.Value, maxItems int) (jsontext.Value, error) {
	buf := &bytes.Buffer{}
	enc := jsontext.NewEncoder(buf)

	if err := trimValue(jsontext.NewDecoder(bytes.NewReader(v)), enc, maxItems); err != nil {
		return nil, err
	}

	return jsontext.Value(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// TrimSchemaExamples applies [TrimExample] to every schema's own Example
// reachable from doc, in place. If maxItems is 0, [DefaultMaxArrayExamples]
// is used.
func TrimSchemaExamples(doc *openapi.Document, maxItems int) error {
	var trimErr error

	trim := func(v jsontext.Value) jsontext.Value {
		if trimErr != nil || len(v) == 0 {
			return v
		}

		trimmed, err := TrimExample(v, maxItems)
		if err != nil {
			trimErr = fmt.Errorf("trimming example: %w", err)
			return v
		}

		return trimmed
	}

	WalkSchemas(doc, func(s *openapi.Schema) {
		s.Example = trim(s.Example)

		for i, ex := range s.Examples {
			s.Examples[i] = trim(ex)
		}
	})

	return trimErr
}

func trimValue(dec *jsontext.Decoder, enc *jsontext.Encoder, maxItems int) error {
	switch dec.PeekKind() {
	case '{':
		return trimObject(dec, enc, maxItems)
	case '[':
		return trimArray(dec, enc, maxItems)
	default:
		tok, err := dec.ReadToken()
		if err != nil {
			return err
		}

		return enc.WriteToken(tok.Clone())
	}
}

func trimObject(dec *jsontext.Decoder, enc *jsontext.Encoder, maxItems int) error {
	if _, err := dec.ReadToken(); err != nil { // '{'
		return err
	}

	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return err
		}

		if err := enc.WriteToken(jsontext.String(key.String())); err != nil {
			return err
		}

		if err := trimValue(dec, enc, maxItems); err != nil {
			return err
		}
	}

	if _, err := dec.ReadToken(); err != nil { // '}'
		return err
	}

	return enc.WriteToken(jsontext.EndObject)
}

func trimArray(dec *jsontext.Decoder, enc *jsontext.Encoder, maxItems int) error {
	// Every element has to be seen before any of them are written: which
	// ones are representative depends on the shapes of all of them.
	var elems []jsontext.Value

	if _, err := dec.ReadToken(); err != nil { // '['
		return err
	}

	for dec.PeekKind() != ']' {
		v, err := dec.ReadValue()
		if err != nil {
			return err
		}

		elems = append(elems, slices.Clone(jsontext.Value(v)))
	}

	if _, err := dec.ReadToken(); err != nil { // ']'
		return err
	}

	kept, err := representative(elems, maxItems)
	if err != nil {
		return err
	}

	if err := enc.WriteToken(jsontext.BeginArray); err != nil {
		return err
	}

	for _, elem := range kept {
		// recurse: an object or array nested inside a kept element is
		// shortened the same way the top-level value is.
		trimmed, err := trimJSON(elem, maxItems)
		if err != nil {
			return err
		}

		if err := enc.WriteValue(trimmed); err != nil {
			return err
		}
	}

	return enc.WriteToken(jsontext.EndArray)
}

// representative selects up to maxItems of elems: first one element per
// distinct [shapeSignature] observed, in order, then whichever elements
// come next, until the budget is used or elems runs out. The result keeps
// elems' own relative order.
func representative(elems []jsontext.Value, maxItems int) ([]jsontext.Value, error) {
	if len(elems) <= maxItems {
		return elems, nil
	}

	keep := make(map[int]bool, maxItems)
	seen := make(map[string]bool, maxItems)

	for i, e := range elems {
		if len(keep) >= maxItems {
			break
		}

		sig, err := shapeSignature(e)
		if err != nil {
			return nil, err
		}

		if seen[sig] {
			continue
		}

		seen[sig] = true
		keep[i] = true
	}

	for i := range elems {
		if len(keep) >= maxItems {
			break
		}

		keep[i] = true
	}

	kept := make([]jsontext.Value, 0, len(keep))
	for i, e := range elems {
		if keep[i] {
			kept = append(kept, e)
		}
	}

	return kept, nil
}

// shapeSignature identifies v's JSON shape for the purpose of picking
// representative array elements: an object's own set of keys (sorted, not
// its values), or a scalar's JSON type. Two elements with the same
// signature are similar enough that keeping just one of them loses nothing
// [TrimExample] cares about.
func shapeSignature(v jsontext.Value) (string, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(v))

	switch dec.PeekKind() {
	case '{':
		if _, err := dec.ReadToken(); err != nil {
			return "", err
		}

		var keys []string
		for dec.PeekKind() != '}' {
			key, err := dec.ReadToken()
			if err != nil {
				return "", err
			}

			keys = append(keys, key.String())

			if _, err := dec.ReadValue(); err != nil { // skip the value
				return "", err
			}
		}

		slices.Sort(keys)

		return "object:" + strings.Join(keys, ","), nil
	case '[':
		return "array", nil
	case '"':
		return "string", nil
	case '0':
		return "number", nil
	case 't', 'f':
		return "boolean", nil
	case 'n':
		return "null", nil
	default:
		return "", fmt.Errorf("unexpected JSON kind %q", dec.PeekKind())
	}
}
