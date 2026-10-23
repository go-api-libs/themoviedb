package enrich

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"time"
	"uuid"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi"
	edit "github.com/MarkRosemaker/openapi-edit"
	merge "github.com/MarkRosemaker/openapi-merge"
	apitypes "github.com/go-api-libs/types"
)

// newSchemaFromJSON infers an OpenAPI schema from a JSON-encoded value.
func newSchemaFromJSON(data []byte) (*openapi.Schema, error) {
	return decodeSchema(jsontext.NewDecoder(bytes.NewReader(data)))
}

func decodeSchema(dec *jsontext.Decoder) (*openapi.Schema, error) {
	v, err := dec.ReadToken()
	if err != nil {
		return nil, err
	}

	switch v.Kind() {
	case '"': // string
		s := &openapi.Schema{
			Type:   openapi.TypeString,
			Format: stringFormat(v.String()),
		}

		// A redacted value makes a poor example: it says nothing the inferred
		// format does not already say, and reads as though the API returns
		// asterisks. Masking is shape-preserving, so the format survives without
		// it.
		if !cassette.IsMasked(v.String()) {
			s.Example = jsontext.Value(fmt.Appendf(nil, "%q", v.String()))
		}

		return s, nil

	case '0': // number
		str := v.String()
		if _, err := strconv.ParseInt(str, 10, 64); err == nil {
			return &openapi.Schema{Type: openapi.TypeInteger, Example: jsontext.Value(str)}, nil
		}

		return &openapi.Schema{Type: openapi.TypeNumber, Format: openapi.FormatDouble, Example: jsontext.Value(str)}, nil

	case 't': // true
		return &openapi.Schema{Type: openapi.TypeBoolean}, nil
	case 'f': // false
		return &openapi.Schema{Type: openapi.TypeBoolean}, nil
	case 'n': // null: merged with a real type later, it makes that type nullable
		return &openapi.Schema{Type: openapi.TypeNull}, nil
	case '{': // begin object
		return decodeObjectSchema(dec)
	case '[': // begin array
		return decodeArraySchema(dec)
	default:
		return nil, fmt.Errorf("unexpected token type %s", v.Kind())
	}
}

func decodeObjectSchema(dec *jsontext.Decoder) (*openapi.Schema, error) {
	type kv struct {
		key    string
		schema *openapi.Schema
	}

	var pairs []kv

	for dec.PeekKind() != '}' {
		keyTok, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}

		if keyTok.Kind() != '"' {
			return nil, fmt.Errorf("expected string key, got %s", keyTok)
		}

		key := keyTok.String()

		propSchema, err := decodeSchema(dec)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", key, err)
		}

		// A redacted number is indistinguishable from a real one by value, so
		// drop the example by key instead. These keys hold credentials, which
		// have no business appearing as examples either way.
		if cassette.RedactsBodyKey(key) {
			propSchema.Example = nil
		}

		pairs = append(pairs, kv{key, propSchema})
	}

	if _, err := dec.ReadToken(); err != nil { // consume '}'
		return nil, err
	}

	if len(pairs) == 0 {
		return &openapi.Schema{Type: openapi.TypeObject}, nil
	}

	// If every key is a stringified non-negative integer the object is a
	// numeric-keyed map (e.g. {"0":{...},"1":{...}}). Model it with
	// additionalProperties so the key pattern is explicit and the schema stays
	// compact regardless of how many entries are present.
	allNumeric := true
	for _, p := range pairs {
		if !isNumericKey(p.key) {
			allNumeric = false
			break
		}
	}

	if allNumeric {
		var valueSchema *openapi.Schema
		for _, p := range pairs {
			if valueSchema == nil {
				valueSchema = p.schema
				continue
			}

			if err := merge.Schema(valueSchema, p.schema, false); err != nil {
				return nil, fmt.Errorf("merging additionalProperties value: %w", err)
			}
		}

		return &openapi.Schema{
			Type:                 openapi.TypeObject,
			AdditionalProperties: &openapi.AdditionalProperties{Schema: valueSchema},
		}, nil
	}

	// Named properties — build the schema as before.
	s := &openapi.Schema{
		Type:       openapi.TypeObject,
		Properties: openapi.Schemas{},
	}
	for _, p := range pairs {
		s.Properties.Set(p.key, p.schema)
		s.Required = append(s.Required, p.key)
	}

	return s, nil
}

// isNumericKey reports whether s consists entirely of ASCII digits.
func isNumericKey(s string) bool {
	if len(s) == 0 {
		return false
	}

	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}

func decodeArraySchema(dec *jsontext.Decoder) (*openapi.Schema, error) {
	s := &openapi.Schema{Type: openapi.TypeArray}

	var elems []*openapi.Schema
	for dec.PeekKind() != ']' {
		elem, err := decodeSchema(dec)
		if err != nil {
			return nil, err
		}

		elems = append(elems, elem)
	}

	if _, err := dec.ReadToken(); err != nil { // consume ']'
		return nil, err
	}

	if len(elems) == 0 {
		// seen empty, so nothing is known of its items; merged with a non-empty array later, it takes that one's
		s.MaxItems = new(uint(0))
		return s, nil
	}

	if item, ok := mergeHomogeneous(elems); ok {
		s.Items = item

		// objects may be variants of a union, each to be routed to its own, so they meet the specification apart
		if len(elems) > 1 && item.Type == openapi.TypeObject {
			s.Items = merge.Samples(distinctSchemas(elems)...)
		}

		return s, nil
	}

	// elems can't merge into one schema: a fixed-size, positionally-typed
	// array (e.g. OpenSky's state vectors: [icao24 string, ..., time_position
	// int, ..., on_ground bool, ...]) mixes types by position, which
	// prefixItems -- not items -- is meant to describe.
	s.PrefixItems = make(openapi.SchemaList, len(elems))
	for i, elem := range elems {
		s.PrefixItems[i] = elem
	}

	return s, nil
}

// distinctSchemas returns l without the schemas equal to one before them.
func distinctSchemas(l []*openapi.Schema) []*openapi.Schema {
	seen := map[string]bool{}

	return slices.DeleteFunc(slices.Clone(l), func(s *openapi.Schema) bool {
		data, err := json.Marshal(s, json.Deterministic(true))
		if err != nil {
			return false
		}

		if seen[string(data)] {
			return true
		}

		seen[string(data)] = true

		return false
	})
}

// collapseSamples replaces every union of samples left in doc, those nothing in the specification routed, by the one
// schema its samples merge into, or by a plain union of them if they cannot.
func collapseSamples(doc *openapi.Document) {
	edit.WalkSchemas(doc, func(s *openapi.Schema) {
		if !merge.IsSamples(s) {
			return
		}

		if item, ok := mergeHomogeneous(s.AnyOf); ok {
			*s = *item
			return
		}

		s.Extensions = nil
	})
}

// mergeHomogeneous attempts to merge every element into a single schema,
// succeeding whenever they describe a plain, arbitrary-length list; ok is
// false the moment two elements' types genuinely disagree. It works on
// clones throughout: decodeArraySchema needs elems left untouched to fall
// back to prefixItems on failure, and merge.Schema mutates both of its
// arguments even when it ultimately returns an error partway through.
func mergeHomogeneous(elems []*openapi.Schema) (_ *openapi.Schema, ok bool) {
	item, err := cloneSchema(elems[0])
	if err != nil {
		return nil, false
	}

	for _, elem := range elems[1:] {
		clone, err := cloneSchema(elem)
		if err != nil {
			return nil, false
		}

		if err := merge.Schema(item, clone, false); err != nil {
			return nil, false
		}
	}

	return item, true
}

// cloneSchema deep-copies s via a JSON round-trip.
func cloneSchema(s *openapi.Schema) (*openapi.Schema, error) {
	data, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}

	clone := &openapi.Schema{}
	if err := json.Unmarshal(data, clone); err != nil {
		return nil, err
	}

	return clone, nil
}

// stringFormat detects the special format for a string value.
// It tries in order: UUID, URI, Email, DateTime (RFC3339), Date (2006-01-02), IPv4/IPv6.
func stringFormat(s string) openapi.Format {
	if isUUID(s) {
		return openapi.FormatUUID
	}

	if u, err := url.Parse(s); err == nil && u.Scheme != "" && u.Host != "" {
		return openapi.FormatURI
	}

	if apitypes.Email(s).Validate() == nil {
		return openapi.FormatEmail
	}

	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return openapi.FormatDateTime
	}

	if _, err := time.Parse(time.DateOnly, s); err == nil {
		return openapi.FormatDate
	}

	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() != nil {
			return openapi.FormatIPv4
		}

		return openapi.FormatIPv6
	}

	return ""
}

// isUUID reports whether s matches the UUID format.
func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

// deref is the schema s stands for: the one it refers to, if it is a reference.
func deref(s *openapi.Schema) *openapi.Schema {
	if s != nil && s.Ref != nil {
		return s.Ref.Value
	}

	return s
}
