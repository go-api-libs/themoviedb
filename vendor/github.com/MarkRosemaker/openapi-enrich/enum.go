package enrich

import (
	"bytes"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"maps"
	"slices"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// growEnums walks s and the raw JSON body it was inferred from in parallel,
// and for every leaf schema that already carries a non-empty Enum, adds any
// newly observed value not already present.
//
// It never sets Enum on a schema that does not already have one: an enum is
// something a maintainer declares by giving a field at least one value, not
// something a recording invents on its own — decodeSchema never sets Enum
// for exactly that reason. This only extends a declaration already made.
//
// That extension needs its own pass over the raw body because merging alone
// cannot see far enough: a schema keeps at most one Example, so a response
// whose array has 90 elements only ever carries the first one's value
// through the ordinary merge — the other 89 are gone before an
// enum-bearing schema anywhere in the tree gets a chance to compare against
// them. Walking the raw body directly, alongside the schema it was merged
// into, sees every element instead of just the one that survived merging.
func growEnums(s *openapi.Schema, raw []byte) error {
	if s == nil || len(raw) == 0 {
		return nil
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil // not JSON (or malformed) — nothing to grow enums from
	}

	return growEnumsValue(s, v)
}

func growEnumsValue(s *openapi.Schema, v any) error {
	if s == nil || v == nil {
		return nil
	}

	switch s.Type {
	case openapi.TypeObject:
		if obj, ok := v.(map[string]any); ok {
			return growEnumsObject(s, obj)
		}
	case openapi.TypeArray:
		arr, ok := v.([]any)
		if !ok || s.Items == nil {
			return nil
		}

		for i, elem := range arr {
			if err := growEnumsValue(deref(s.Items), elem); err != nil {
				return &errpath.ErrIndex{Index: i, Err: err}
			}
		}
	default:
		if len(s.Enum) > 0 {
			return addEnumValue(s, v)
		}
	}

	return nil
}

// growEnumsObject grows the enums of an object's properties, of the values of a map and of its keys.
func growEnumsObject(s *openapi.Schema, obj map[string]any) error {
	for key, propRef := range s.Properties.ByIndex() {
		val, ok := obj[key]
		if !ok {
			continue
		}

		if err := growEnumsValue(deref(propRef), val); err != nil {
			return &errpath.ErrField{Field: "properties", Err: &errpath.ErrKey{Key: key, Err: err}}
		}
	}

	// a map does not keep the order the keys were recorded in, so new values are added in the keys' order
	keys := slices.Sorted(maps.Keys(obj))

	if ap := s.AdditionalProperties; ap != nil && ap.Schema != nil {
		for _, key := range keys {
			if cassette.RedactsBodyKey(key) {
				continue
			}

			if err := growEnumsValue(deref(ap.Schema), obj[key]); err != nil {
				return &errpath.ErrField{Field: "additionalProperties", Err: &errpath.ErrKey{Key: key, Err: err}}
			}
		}
	}

	// masking hides a value, never its key, so every key observed is a real one
	if s.PropertyNames != nil {
		for _, key := range keys {
			if err := growEnumsValue(deref(s.PropertyNames), key); err != nil {
				return &errpath.ErrField{Field: "propertyNames", Err: &errpath.ErrKey{Key: key, Err: err}}
			}
		}
	}

	return nil
}

// addEnumValue adds v to s.Enum if an equal value is not already present.
// Values are compared canonicalized — the same order-independent,
// whitespace-independent JSON equality openapi-merge uses when comparing an
// Enum against a candidate Example while merging two schemas.
func addEnumValue(s *openapi.Schema, v any) error {
	if str, ok := v.(string); ok && cassette.IsMasked(str) {
		return nil // a masked value teaches nothing and has no business in a public enum
	}

	data, err := json.Marshal(v)
	if err != nil {
		return err
	}

	candidate := jsontext.Value(data)
	if err := candidate.Canonicalize(); err != nil {
		return err
	}

	for _, e := range s.Enum {
		existing := e.Clone()
		if err := existing.Canonicalize(); err != nil {
			return err
		}

		if bytes.Equal(existing, candidate) {
			return nil
		}
	}

	s.Enum = append(s.Enum, candidate)

	return nil
}
