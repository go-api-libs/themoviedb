package merge

import (
	"bytes"
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// Schema merges b into a, so that a describes every value either one did.
// b may be mutated too (e.g. when a turns out to be the one generated from
// null, or when the two disagree on type in a reconcilable way), but a is
// always the result: callers should keep using a afterward, not b.
func Schema(a, b *openapi.Schema, isParam bool) error {
	a.Title = mergeString(a.Title, b.Title)
	a.Description = mergeString(a.Description, b.Description)

	if handled, err := mergeIfSamples(a, b); handled {
		return err
	}

	if handled, err := mergeIfOneOf(a, b); handled {
		return err
	}

	if handled, err := mergeIfUnionInAllOf(a, b); handled {
		return err
	}

	tp, err := effectiveType(a)
	if err != nil {
		return err
	}

	if b.Type == openapi.TypeNull {
		mergeNull(a)
		return nil
	}

	tp = reconcileNull(a, b, tp)
	improveExample(a, b)

	if tp != b.Type {
		handled, err := reconcileTypeMismatch(a, b, tp, isParam)
		if err != nil {
			return err
		}

		if handled {
			return nil
		}
	}

	if isDateAndDateTime(a, b) {
		mergeDateOrDateTime(a, b)
		return nil
	}

	if err := reconcileFormats(a, b); err != nil {
		return err
	}

	if err := mergeEnumFromExample(a, b); err != nil {
		return err
	}

	return mergeByType(a, b, tp)
}

// mergeIfOneOf merges b into a, or a into b, when either already represents
// multiple possible shapes (e.g. a date that can be either a date-time
// string or a unix timestamp integer) -- see [mergeOneOf]. handled reports
// whether this fully resolved the merge, in which case err (nil or not) is
// Schema's own result.
func mergeIfOneOf(a, b *openapi.Schema) (handled bool, err error) {
	// if a already represents multiple possible shapes, merge b into
	// whichever alternative it matches
	if alts, field := union(a); len(alts) > 0 {
		if newArrayShape(alts, b) {
			c := *b
			c.Title, c.Description = "", ""

			if field == "oneOf" {
				a.OneOf = append(a.OneOf, &c)
			} else {
				a.AnyOf = append(a.AnyOf, &c)
			}

			return true, nil
		}

		return true, mergeAlternatives(alts, field, b)
	}

	// symmetric case: b is the one that already has multiple possible shapes.
	// a's Title/Description were already merged into a above; carry that
	// over to b, since b (not a) is about to become the authoritative result.
	if alts, field := union(b); len(alts) > 0 {
		b.Title, b.Description = a.Title, a.Description

		if err := mergeAlternatives(alts, field, a); err != nil {
			return true, err
		}

		a.Replace(b)

		return true, nil
	}

	return false, nil
}

// newArrayShape reports whether b is an array of a shape, list or tuple of a length, that no array among alts has,
// which then needs an alternative of its own beside them, as merging it into one would nest another union.
func newArrayShape(alts openapi.SchemaList, b *openapi.Schema) bool {
	if b.Type != openapi.TypeArray {
		return false
	}

	arrays := 0

	for _, alt := range alts {
		alt = deref(alt)
		if alt.Type != openapi.TypeArray {
			continue
		}

		if oneOfBranchMatches(alt, b, false) {
			return false
		}

		arrays++
	}

	return arrays > 0
}

// effectiveType is a.Type, or the common object type its allOf resolves to
// when a has one. Only an allOf of object schemas is supported.
func effectiveType(a *openapi.Schema) (openapi.DataType, error) {
	tp := a.Type

	if len(a.AllOf) == 0 {
		return tp, nil
	}

	if tp != "" && tp != openapi.TypeObject {
		return "", fmt.Errorf("type %q with allOf not implemented yet", tp)
	}

	tp = deref(a.AllOf[0]).Type

	if tp != openapi.TypeObject {
		return "", fmt.Errorf("allOf with type %q not implemented yet", tp)
	}

	idx := slices.IndexFunc(a.AllOf[1:], func(s *openapi.Schema) bool {
		return deref(s).Type != tp
	})
	if idx != -1 {
		return "", &errpath.ErrField{
			Field: "allOf",
			Err: &errpath.ErrIndex{
				Index: idx + 1,
				Err: &errpath.ErrField{
					Field: "type",
					Err: fmt.Errorf("%q != %q",
						deref(a.AllOf[idx+1]).Type, tp),
				},
			},
		}
	}

	return tp, nil
}

// mergeNull merges a value seen as null into a: a typed schema becomes nullable, and an untyped one, which already allows null, is left alone.
func mergeNull(a *openapi.Schema) {
	if a.Type != "" && a.Type != openapi.TypeNull {
		a.Nullable = true
	}
}

// reconcileNull makes a, if it was only seen as null, take b's type and allow null too, and carries over b's nullability.
// It returns the type to merge as.
func reconcileNull(a, b *openapi.Schema, tp openapi.DataType) openapi.DataType {
	if a.Type == openapi.TypeNull && b.Type != "" {
		tp = b.Type
		a.Type, a.Format, a.Nullable = tp, b.Format, true
		// a has no bounds of its own to widen b's with
		a.MinItems, a.MaxItems = b.MinItems, b.MaxItems
	}

	a.Nullable = a.Nullable || b.Nullable

	return tp
}

// improveExample keeps a's example unless it has none, or has only "null"
// where b's is a real value. A const gets no example, which could only repeat
// it; a one-value enum still does, since an enum grows from examples.
func improveExample(a, b *openapi.Schema) {
	if len(a.Const) > 0 {
		return
	}

	if a.Example == nil || (string(a.Example) == null && b.Example != nil) {
		a.Example = b.Example
	}
}

// reconcileTypeMismatch handles a and b disagreeing on type, which a strict
// merge would otherwise reject outright.
//
// HACK: allow schemas that don't quite match but were generated by analyzing
// json. The solution lies in first improving apilib or one of its
// dependencies to not generate faulty schemas in the first place -- then
// those schemas can be properly merged.
//
// handled reports whether the merge is now complete. It is false only for
// the isParam/array case, whose own call already rewrote a and b to be
// identical, so the merge continues below into the format and type checks.
func reconcileTypeMismatch(a, b *openapi.Schema, tp openapi.DataType, isParam bool) (handled bool, err error) {
	aJSON, bJSON := jsonString(a), jsonString(b)

	switch {
	case isParam && (a.Type == openapi.TypeArray || b.Type == openapi.TypeArray):
		if err := mergeArrayParamMismatch(a, b); err != nil {
			return true, err
		}

		return false, nil
	case tp == openapi.TypeString && (b.Type == openapi.TypeNumber || b.Type == openapi.TypeInteger) && isNumericString(a),
		tp == openapi.TypeNumber && b.Type == openapi.TypeString && isNumericString(b):
		// an API that sends numbers as strings, such as "9.5", and now and then a number, such as 0.0
		mergeStringOrNumber(a, b)
		return true, nil
	case tp == openapi.TypeNumber && b.Type == openapi.TypeString && isInfinityExample(bJSON),
		b.Type == openapi.TypeNumber && tp == openapi.TypeString && isZeroDoubleExample(bJSON):
		b.Replace(a)
		return true, nil
	case b.Type == openapi.TypeNumber && tp == openapi.TypeString && isInfinityExample(aJSON):
		a.Replace(b)
		return true, nil
	case a.Type == openapi.TypeNumber && b.Type == openapi.TypeInteger:
		b.Replace(a)
		return true, nil
	case a.Type == openapi.TypeInteger && b.Type == openapi.TypeNumber:
		a.Replace(b)
		return true, nil
	case isDateTimeString(a) && b.Type == openapi.TypeInteger,
		isDateTimeString(b) && a.Type == openapi.TypeInteger:
		// a date can be expressed as a date-time string or as a unix
		// timestamp integer; document both possibilities with oneOf
		// rather than silently discarding one of them
		mergeDateTimeOrTimestamp(a, b)
		return true, nil
	case a.Type == openapi.TypeInteger && b.Type == openapi.TypeString:
		b.Replace(a)
		return true, nil
	default:
		return true, mismatchError("type", fmt.Errorf("%q != %q", tp, b.Type), a, b)
	}
}

// widenIntegerFormat gives integers a and b the wider of their formats, int64 over int32 and uint64 over uint32, and a
// 64-bit one where an example does not fit 32 bits.
func widenIntegerFormat(a, b *openapi.Schema) {
	for _, s := range []*openapi.Schema{a, b} {
		var w openapi.Format

		switch s.Format { //nolint:exhaustive // only 32-bit formats widen
		case openapi.FormatInt32:
			w = openapi.FormatInt64
		case openapi.FormatUint32:
			w = openapi.FormatUint64
		default:
			continue
		}

		if a.Format == w || b.Format == w || !fits32(a.Example, s.Format) || !fits32(b.Example, s.Format) {
			a.Format, b.Format = w, w
			return
		}
	}
}

// fits32 reports whether the integer example v, if any, fits the 32-bit format f.
func fits32(v jsontext.Value, f openapi.Format) bool {
	if len(v) == 0 {
		return true
	}

	if f == openapi.FormatUint32 {
		_, err := strconv.ParseUint(string(v), 10, 32)
		return err == nil
	}

	_, err := strconv.ParseInt(string(v), 10, 32)

	return err == nil
}

// fitsDateBasic reports whether the string example v, if any, is a date in ISO 8601's basic form, such as "20260320".
func fitsDateBasic(v jsontext.Value) bool {
	if len(v) == 0 {
		return true
	}

	var str string
	if json.Unmarshal(v, &str) != nil {
		return false
	}

	_, err := time.Parse("20060102", str)

	return err == nil
}

// isEmptyString reports whether v is the empty string.
func isEmptyString(v jsontext.Value) bool { return string(v) == `""` }

// withoutFormat is whichever of a and b has no format.
func withoutFormat(a, b *openapi.Schema) *openapi.Schema {
	if a.Format == "" {
		return a
	}

	return b
}

// reconcileFormats resolves a and b's formats when either is unset, and
// reports a mismatch it could not resolve.
func reconcileFormats(a, b *openapi.Schema) error {
	// if one doesn't conform to the format, we cannot guarantee the format
	if a.Format == "" || b.Format == "" {
		switch a.Type {
		case openapi.TypeInteger:
			// an integer without a format is one of any width: the other's format stays, wide enough for both below
			f := cmp.Or(a.Format, b.Format)
			a.Format, b.Format = f, f
		case openapi.TypeNumber:
			if a.Format != "" {
				b.Format = a.Format
			} else if b.Format != "" {
				a.Format = b.Format
			} else {
				// If no indication given, default to double
				a.Format = openapi.FormatDouble
				b.Format = openapi.FormatDouble
			}
		case openapi.TypeString:
			// date-basic, which no sample is recorded with, stays where the value without a format fits it
			switch f := cmp.Or(a.Format, b.Format); {
			case f == openapi.FormatDateBasic && fitsDateBasic(a.Example) && fitsDateBasic(b.Example):
				a.Format, b.Format = f, f
			case (f == openapi.FormatDate || f == openapi.FormatDateTime) && isEmptyString(withoutFormat(a, b).Example):
				// an empty string is a date or date-time left out, which a client reads as the zero value
				if isEmptyString(a.Example) {
					a.Example = b.Example
				}

				a.Format, b.Format = f, f
			default:
				a.Format, b.Format = "", ""
			}
		default:
			a.Format = ""
			b.Format = ""
		}
	}

	if a.Type == openapi.TypeInteger {
		widenIntegerFormat(a, b)
	}

	// check that the formats are the same
	if a.Format != b.Format {
		return mismatchError("format", fmt.Errorf("%q != %q", a.Format, b.Format), a, b)
	}

	return nil
}

// mergeEnumFromExample adds b's example to a's enum, when a has one and
// doesn't already have it. Enums are no longer string-only, so this applies
// regardless of the schema's type.
func mergeEnumFromExample(a, b *openapi.Schema) error {
	if a.Enum == nil || b.Example == nil {
		return nil
	}

	ex := b.Example.Clone()
	if err := ex.Canonicalize(); err != nil {
		return &errpath.ErrField{Field: "example", Err: err}
	}

	for i, enum := range a.Enum {
		enum = enum.Clone()
		if err := enum.Canonicalize(); err != nil {
			return &errpath.ErrField{
				Field: "enum",
				Err:   &errpath.ErrIndex{Index: i, Err: err},
			}
		}

		if bytes.Equal(enum, ex) {
			return nil
		}
	}

	a.Enum = append(a.Enum, ex)

	return nil
}

// mergeByType finishes the merge according to tp, a and b's now-agreed type.
func mergeByType(a, b *openapi.Schema, tp openapi.DataType) error {
	switch tp {
	case openapi.TypeString: // nothing left to do here; enum handled above
	case openapi.TypeObject:
		mergeRequired(a, b)

		if err := mergeObjectProperties(a, b); err != nil {
			return err
		}
	case openapi.TypeBoolean, openapi.TypeInteger, openapi.TypeNumber: // nothing to do
	case openapi.TypeArray:
		if err := mergeArrayItems(a, b); err != nil {
			return err
		}
	default:
		return mismatchError("type", fmt.Errorf("%q unimplemented", tp), a, b)
	}

	return nil
}

// mergeArrayItems merges b's array shape into a's. When both describe a
// fixed-length tuple of the same length, each position merges against its
// counterpart via PrefixItems, the same way Items already does for a plain
// list. Otherwise -- tuple lengths disagree, or only one side is a tuple at
// all -- positions can't line up, so the result keeps both shapes as
// alternatives instead of forcing a lossy merge between them.
func mergeArrayItems(a, b *openapi.Schema) error {
	mergeItemBounds(a, b)

	switch {
	case len(a.PrefixItems) == 0 && len(b.PrefixItems) == 0:
		return mergeItemsField(a, b)
	case len(a.PrefixItems) == len(b.PrefixItems):
		for i, ai := range a.PrefixItems {
			if err := Schema(deref(ai), deref(b.PrefixItems[i]), false); err != nil {
				return &errpath.ErrField{Field: "prefixItems", Err: &errpath.ErrIndex{Index: i, Err: err}}
			}
		}

		// items still applies to any element beyond the two sides' shared
		// prefixItems length; merge it too when either side sets it.
		if a.Items != nil || b.Items != nil {
			return mergeItemsField(a, b)
		}

		return nil
	case len(b.PrefixItems) == 0 && fitsEveryPosition(a.PrefixItems, b.Items):
		// a sample keeps no length, so a list whose items fit each position may be the tuple
		for i, ai := range a.PrefixItems {
			item, err := cloneSchema(b.Items)
			if err != nil {
				return err
			}

			if err := Schema(deref(ai), item, false); err != nil {
				return &errpath.ErrField{Field: "prefixItems", Err: &errpath.ErrIndex{Index: i, Err: err}}
			}
		}

		return nil
	default:
		mergeArrayShapeMismatch(a, b)
		return nil
	}
}

// fitsEveryPosition reports whether item, the items of a list, has the type of every position of a tuple, or is an
// integer where a position is a number.
func fitsEveryPosition(positions openapi.SchemaList, item *openapi.Schema) bool {
	if item == nil || item.Ref != nil || item.Type == "" {
		return false
	}

	return !slices.ContainsFunc(positions, func(p *openapi.Schema) bool {
		p = deref(p)

		return p == nil || p.Type != item.Type && (p.Type != openapi.TypeNumber || item.Type != openapi.TypeInteger)
	})
}

// cloneSchema is a deep copy of s.
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

// mergeItemBounds widens a's bounds on the number of items to cover b's too. A side that states no bound says nothing
// of it, as a recorded array does not count its items, so the other's stands; but a maximum of zero only says the
// array was seen empty, and an array seen empty allows no minimum.
func mergeItemBounds(a, b *openapi.Schema) {
	switch {
	case seenEmpty(a) || seenEmpty(b):
		a.MinItems = 0
	case a.MinItems == 0 || b.MinItems == 0:
		a.MinItems = max(a.MinItems, b.MinItems)
	default:
		a.MinItems = min(a.MinItems, b.MinItems)
	}

	switch {
	case a.MaxItems != nil && b.MaxItems != nil:
		a.MaxItems = new(max(*a.MaxItems, *b.MaxItems))
	case a.MaxItems == nil && b.MaxItems != nil && *b.MaxItems > 0:
		a.MaxItems = b.MaxItems
	case a.MaxItems != nil && b.MaxItems == nil && *a.MaxItems == 0:
		a.MaxItems = nil
	}
}

// seenEmpty reports whether the array s allows no items: one recorded only empty.
func seenEmpty(s *openapi.Schema) bool { return s.MaxItems != nil && *s.MaxItems == 0 }

// mergeItemsField merges a.Items with b.Items. Either side may be nil,
// meaning that side never saw inside the array at all -- e.g. because it was
// reconciled up from a value that was simply null in that sample -- in which
// case the result adopts the other side's outright, rather than merging
// against a fabricated placeholder that would otherwise claim to have
// observed something (like a null element) that was never actually seen.
func mergeItemsField(a, b *openapi.Schema) error {
	switch {
	case a.Items == nil:
		a.Items = b.Items
		return nil
	case b.Items == nil:
		b.Items = a.Items
		return nil
	default:
		return Schema(deref(a.Items), deref(b.Items), false)
	}
}

// mergeArrayShapeMismatch documents a and b -- both arrays whose shapes
// can't be reconciled positionally -- as two alternatives via oneOf,
// mirroring mergeDateTimeOrTimestamp: forcing an incompatible merge here
// would silently drop one shape's information.
func mergeArrayShapeMismatch(a, b *openapi.Schema) {
	aCopy, bCopy := *a, *b
	aCopy.Title, aCopy.Description = "", ""
	bCopy.Title, bCopy.Description = "", ""

	merged := openapi.Schema{
		Title:       a.Title,
		Description: a.Description,
		OneOf: openapi.SchemaList{
			&aCopy,
			&bCopy,
		},
	}

	a.Replace(&merged)
	b.Replace(&merged)
}

// mergeObjectProperties merges b's properties into a's, either directly or,
// when a is a string map, into its additionalProperties schema.
func mergeObjectProperties(a, b *openapi.Schema) error {
	if ap := a.AdditionalProperties; ap != nil && ap.Schema != nil {
		// is a string map
		if bp := b.AdditionalProperties; bp != nil && bp.Schema != nil {
			if err := Schema(deref(ap.Schema), deref(bp.Schema), false); err != nil {
				return &errpath.ErrField{Field: "additionalProperties", Err: err}
			}
		}

		// merge all property values with prop, in the order they were recorded, since the first example is the one kept
		for _, prop := range b.Properties.ByIndex() {
			if err := Schema(deref(ap.Schema), deref(prop), false); err != nil {
				return &errpath.ErrField{Field: "additionalProperties", Err: err}
			}
		}
	} else {
		a.AdditionalProperties = mergeAdditionalProperties(a.AdditionalProperties, b.AdditionalProperties)

		// Ensure a.Properties is initialized so schemas can append to it
		// when a has no properties yet but b does.
		if a.Properties == nil && len(b.Properties) > 0 {
			a.Properties = openapi.Schemas{}
		}

		// get the maps that contains all properties
		allProps := maps.Clone(a.Properties)
		for _, allOf := range a.AllOf {
			// only looked up, so no index to set
			maps.Insert(allProps, deref(allOf).Properties.ByIndex())
		}

		// get the map that we should add properties from b from
		addNewProps := a.Properties
		field := "properties"
		if len(a.AllOf) > 0 {
			field = "allOf"
			addNewProps = openapi.Schemas{}
		}

		if err := schemas(allProps, &addNewProps, b.Properties); err != nil {
			return &errpath.ErrField{Field: field, Err: err}
		}

		if len(a.AllOf) > 0 && len(addNewProps) > 0 {
			a.AllOf = append(a.AllOf, &openapi.Schema{
				Type:       openapi.TypeObject,
				Properties: addNewProps,
			})
		}
	}

	// remove properties if they are none
	if len(a.Properties) == 0 {
		a.Properties = nil
	}

	return nil
}

// mergeRequired keeps a requiring only what b requires too, as a value without a member makes it optional: a recorded
// sample requires every member it has. What a's allOf parts require is narrowed the same way.
func mergeRequired(a, b *openapi.Schema) {
	byB := requiredOf(b)

	var narrow func(s *openapi.Schema)

	narrow = func(s *openapi.Schema) {
		if s.Required != nil {
			s.Required = slices.DeleteFunc(s.Required, func(name string) bool { return !byB[name] })
			if len(s.Required) == 0 {
				s.Required = nil
			}
		}

		for _, part := range s.AllOf {
			if part.Ref == nil { // a referenced part is a schema of its own that others may use as it is
				narrow(part)
			}
		}
	}

	narrow(a)
}

// requiredOf returns the members s requires, its allOf parts' included.
func requiredOf(s *openapi.Schema) map[string]bool {
	out := map[string]bool{}
	for _, name := range s.Required {
		out[name] = true
	}

	for _, part := range s.AllOf {
		maps.Copy(out, requiredOf(deref(part)))
	}

	return out
}

// mergeAdditionalProperties merges a non-schema a with b: absent says nothing, a schema beats a boolean, and true beats false.
func mergeAdditionalProperties(a, b *openapi.AdditionalProperties) *openapi.AdditionalProperties {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Schema != nil:
		return b
	default:
		return &openapi.AdditionalProperties{Allowed: a.Allowed || b.Allowed}
	}
}

// jsonString marshals s for a debug message or a shape comparison against
// [infinityExample]/[zeroDoubleExample]. Only called on the type-mismatch
// path, never on an ordinary merge, so it does not need to be cheap.
func jsonString(s *openapi.Schema) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// mismatchError builds the error for a merge that could not be resolved,
// naming field and wrapping err, with a and b's own marshaled JSON appended
// so whoever reads the error can see the schemas that did not match without
// having to reproduce the merge.
func mismatchError(field string, err error, a, b *openapi.Schema) error {
	return &errpath.ErrField{
		Field: field,
		Err:   fmt.Errorf("%w\na: %s\nb: %s", err, jsonString(a), jsonString(b)),
	}
}

var null = jsontext.Null.String()

// isDateTimeString reports whether the schema is a string with a date or date-time format.
func isDateTimeString(s *openapi.Schema) bool {
	return s.Type == openapi.TypeString &&
		(s.Format == openapi.FormatDate || s.Format == openapi.FormatDateTime)
}

const (
	// infinityExample is the schema generated for a floating point field whose
	// only observed value overflowed to "Infinity" and was therefore encoded as a string.
	infinityExample = `{"type":"string","example":"Infinity"}`
	// zeroDoubleExample is the schema generated for a floating point field whose
	// only observed value was exactly 0.0.
	zeroDoubleExample = `{"type":"number","format":"double","example":0.0}`
)

func isInfinityExample(marshaledSchema string) bool {
	return marshaledSchema == infinityExample
}

func isZeroDoubleExample(marshaledSchema string) bool {
	return marshaledSchema == zeroDoubleExample
}

// mergeArrayParamMismatch merges a parameter that was generated as an array in
// one sample and as a plain value in another, by merging the plain value into
// the array's item schema.
func mergeArrayParamMismatch(a, b *openapi.Schema) error {
	if a.Type == openapi.TypeArray {
		if err := Schema(deref(a.Items), b, false); err != nil {
			return fmt.Errorf("item of param: %w", err)
		}

		if a.Example != nil {
			s := ""
			if err := json.Unmarshal(a.Example, &s); err == nil {
				a.Example = jsontext.Value(fmt.Sprintf("[%q]", s))
			}
		}

		b.Replace(a)

		return nil
	}

	if err := Schema(a, deref(b.Items), false); err != nil {
		return fmt.Errorf("item of param: %w", err)
	}

	if b.Example != nil {
		s := ""
		if err := json.Unmarshal(b.Example, &s); err == nil {
			b.Example = jsontext.Value(fmt.Sprintf("[%q]", s))
		}
	}

	a.Replace(b)

	return nil
}

// union is the alternatives of s if it is a union, oneOf before anyOf, with the keyword that lists them.
func union(s *openapi.Schema) (openapi.SchemaList, string) {
	if len(s.OneOf) > 0 {
		return s.OneOf, "oneOf"
	}

	return s.AnyOf, "anyOf"
}

// mergeOneOf merges b into the alternative of a.OneOf that matches it,
// since a already represents a value that can take multiple shapes.
func mergeOneOf(a, b *openapi.Schema) error {
	return mergeAlternatives(a.OneOf, "oneOf", b)
}

// mergeAlternatives merges b into the one of alts that matches it -- see
// [matchingAlternative] -- reporting errors under field, the keyword alts are
// listed in.
func mergeAlternatives(alts openapi.SchemaList, field string, b *openapi.Schema) error {
	// b is itself a union (e.g. built up from another, independent set of
	// samples that happened to hit the same alternatives); merge each of its
	// alternatives into a in turn rather than treating b as a single schema
	if bAlts, bField := union(b); len(bAlts) > 0 {
		for i, alt := range bAlts {
			if err := mergeAlternatives(alts, field, deref(alt)); err != nil {
				return &errpath.ErrField{Field: bField, Err: &errpath.ErrIndex{Index: i, Err: err}}
			}
		}

		return nil
	}

	// b carries no real type information (e.g. it was generated from a null
	// value in this particular sample); there's no way to tell which
	// alternative it would belong to, so there's nothing to merge
	if b.Type == "" || b.Type == openapi.TypeNull {
		return nil
	}

	idx, err := matchingAlternative(alts, b)
	if err != nil {
		return &errpath.ErrField{Field: field, Err: err}
	}

	if err := mergeIntoAlternative(deref(alts[idx]), b); err != nil {
		return &errpath.ErrField{Field: field, Err: &errpath.ErrIndex{Index: idx, Err: err}}
	}

	return nil
}

// mergeIntoAlternative merges b into alt, one alternative of a union.
func mergeIntoAlternative(alt, b *openapi.Schema) error {
	// the title and description belong on the union itself, not on the
	// individual alternatives; undo whatever the merge picks up from b, but
	// keep what alt had, which may be a component's own
	title, desc := alt.Title, alt.Description
	defer func() { alt.Title, alt.Description = title, desc }()

	return Schema(alt, b, false)
}

// matchingAlternative is the index of the one of alts b is an instance of.
//
// Among alternatives of b's type, one that pins properties to a single value
// -- with const or a one-value enum, the way a tagged union's "type" names
// its variant -- matches only if b agrees with each of those values (see
// [pinsAgree]), and wins if b holds one of them. One that pins none, or none b
// holds, matches any b of its type, and is the fallback when no alternative's
// pinned properties match: the one b fits best.
func matchingAlternative(alts openapi.SchemaList, b *openapi.Schema) (int, error) {
	discriminators := map[string]bool{}

	idx, _, _, ok := matchAlternative(alts, b, false, discriminators)
	if !ok && b.Type == openapi.TypeString {
		// an alternative without a format takes any string, when none has b's
		idx, _, _, ok = matchAlternative(alts, b, true, discriminators)
	}

	if ok {
		return idx, nil
	}

	if len(discriminators) == 0 {
		return -1, fmt.Errorf("no branch matches type %q", b.Type)
	}

	got := make([]string, 0, len(discriminators))
	for _, name := range slices.Sorted(maps.Keys(discriminators)) {
		v := "none"
		if p, ok := b.Properties[name]; ok && valueOf(deref(p)) != nil {
			v = string(valueOf(deref(p)))
		}

		got = append(got, name+": "+v)
	}

	return -1, fmt.Errorf("no branch matches %s", strings.Join(got, ", "))
}

// matchAlternative is the search [matchingAlternative] makes, with anyFormat letting an alternative of strings
// without a format take a string of any format -- see [oneOfBranchMatches]. It reports whether one matches, and
// whether through its pinned properties. Of the alternatives that match through them, it picks the one that declares
// the most of b's properties: a partial object pins what the full one does, but declares less of what was recorded.
// An alternative that is itself a union matches as its own best alternative does. It adds the names of the pinned
// properties that did not match to discriminators.
func matchAlternative(
	alts openapi.SchemaList, b *openapi.Schema, anyFormat bool, discriminators map[string]bool,
) (idx, fit int, pinned, ok bool) {
	best, bestFit, fallback, fallbackFit := -1, -1, -1, -1

	for i, alt := range alts {
		alt = deref(alt)

		union := alt.OneOf
		if len(union) == 0 {
			union = alt.AnyOf
		}

		if len(union) > 0 {
			// a union competes as its best alternative does: by its pinned properties, or else by how well b fits it
			if _, f, p, ok := matchAlternative(union, b, anyFormat, discriminators); p {
				if f > bestFit {
					best, bestFit = i, f
				}
			} else if ok && (fallback == -1 || f > fallbackFit) {
				fallback, fallbackFit = i, f
			}

			continue
		}

		if !oneOfBranchMatches(alt, b, anyFormat) {
			continue
		}

		values := pinnedProperties(alt)
		if len(values) == 0 {
			// of the branches that pin nothing, the one that fits b best, not merely the first
			if f := fitOf(alt, b); fallback == -1 || f > fallbackFit {
				fallback, fallbackFit = i, f
			}

			continue
		}

		agrees, evidence := pinsAgree(alt, b, values)
		if !agrees {
			for name := range values {
				discriminators[name] = true
			}

			continue
		}

		if !evidence {
			// nothing b holds names this branch, nor contradicts it: it competes as one that pins nothing
			if f := fitOf(alt, b); fallback == -1 || f > fallbackFit {
				fallback, fallbackFit = i, f
			}

			continue
		}

		if f := declared(alt, b); f > bestFit {
			best, bestFit = i, f
		}
	}

	if best != -1 {
		return best, bestFit, true, true
	}

	return fallback, fallbackFit, false, fallback != -1
}

// fitOf is how well b fits the branch s: twice the number of b's properties s declares, and one more if b has every
// property s requires, which tells apart branches that declare as many.
func fitOf(s, b *openapi.Schema) int {
	f := 2 * declared(s, b)
	if !lacksRequired(s, b) {
		f++
	}

	return f
}

// lacksRequired reports whether b, an object, lacks a property the object s requires.
func lacksRequired(s, b *openapi.Schema) bool {
	if b.Type != openapi.TypeObject {
		return false
	}

	for name := range requiredOf(s) {
		if _, ok := b.Properties[name]; !ok {
			return true
		}
	}

	return false
}

// declared is the number of b's properties s, or the parts of its allOf, declares.
func declared(s, b *openapi.Schema) int {
	n := 0

	for name := range b.Properties {
		if slices.ContainsFunc(append(openapi.SchemaList{s}, s.AllOf...), func(part *openapi.Schema) bool {
			_, ok := deref(part).Properties[name]
			return ok
		}) {
			n++
		}
	}

	return n
}

// pinnedProperties are the properties s, or the parts of its allOf, allows
// only one value for, with that value.
func pinnedProperties(s *openapi.Schema) map[string]jsontext.Value {
	pinned := map[string]jsontext.Value{}

	for _, part := range append(openapi.SchemaList{s}, s.AllOf...) {
		for name, p := range deref(part).Properties {
			p = deref(p)
			switch {
			case len(p.Const) > 0:
				pinned[name] = p.Const
			case len(p.Enum) == 1:
				pinned[name] = p.Enum[0]
			}
		}
	}

	return pinned
}

// pinsAgree reports whether b agrees with the values s pins its properties to, and whether any of them is evidence
// that b is an s. A property b has with the pinned value is both; one s does not require and b lacks, or one b has
// with no value recorded, such as a boolean, but of the pinned value's type, agrees without being evidence.
func pinsAgree(s, b *openapi.Schema, values map[string]jsontext.Value) (agrees, evidence bool) {
	required := requiredOf(s)

	for name, want := range values {
		p, ok := b.Properties[name]
		if !ok {
			if required[name] {
				return false, false
			}

			continue
		}

		p = deref(p)

		v := valueOf(p)
		if v == nil {
			if !typeAgrees(p.Type, want) {
				return false, false
			}

			continue
		}

		if !equalJSON(v, want) {
			return false, false
		}

		evidence = true
	}

	return true, evidence
}

// typeAgrees reports whether a value of type tp can be v.
func typeAgrees(tp openapi.DataType, v jsontext.Value) bool {
	switch v.Kind() {
	case '"':
		return tp == openapi.TypeString
	case 't', 'f':
		return tp == openapi.TypeBoolean
	case '0':
		return tp == openapi.TypeNumber || tp == openapi.TypeInteger
	case 'n':
		return tp == openapi.TypeNull
	default:
		return false
	}
}

// valueOf is the one value s says a property has: its const, its only enum
// value, or, for a schema inferred from a single sample, its example.
func valueOf(s *openapi.Schema) jsontext.Value {
	switch {
	case len(s.Const) > 0:
		return s.Const
	case len(s.Enum) == 1:
		return s.Enum[0]
	default:
		return s.Example
	}
}

// equalJSON reports whether a and b are the same JSON value.
func equalJSON(a, b jsontext.Value) bool {
	if a == nil || b == nil {
		return false
	}

	a, b = a.Clone(), b.Clone()

	return a.Canonicalize() == nil && b.Canonicalize() == nil && bytes.Equal(a, b)
}

// mergeIfUnionInAllOf merges b, an object, into a when a is an allOf of
// object parts and one union of objects -- the way a tagged union whose
// variants share common properties is often written. Each of b's properties
// that a part declares is merged into that part; the rest, into the variant
// of the union b matches. handled reports whether a was of that shape, in
// which case err is Schema's own result.
func mergeIfUnionInAllOf(a, b *openapi.Schema) (handled bool, err error) {
	if len(a.AllOf) == 0 || b.Type != openapi.TypeObject {
		return false, nil
	}

	unionIdx := -1

	for i, part := range a.AllOf {
		if part = deref(part); len(part.OneOf) == 0 && len(part.AnyOf) == 0 {
			continue
		}

		if unionIdx != -1 {
			return true, &errpath.ErrField{Field: "allOf", Err: errors.New("more than one union not implemented yet")}
		}

		unionIdx = i
	}

	if unionIdx == -1 {
		return false, nil
	}

	declared := map[string]bool{}

	for i, part := range a.AllOf {
		if i == unionIdx {
			continue
		}

		part = deref(part)

		props := propertiesOf(b, func(name string) bool { _, ok := part.Properties[name]; return ok })
		if len(props) == 0 {
			continue
		}

		for name := range props {
			declared[name] = true
		}

		if err := Schema(part, &openapi.Schema{Type: openapi.TypeObject, Properties: props, Required: requiredAmong(b, props)}, false); err != nil {
			return true, &errpath.ErrField{Field: "allOf", Err: &errpath.ErrIndex{Index: i, Err: err}}
		}
	}

	union := deref(a.AllOf[unionIdx])
	alts, field := union.OneOf, "oneOf"

	if len(alts) == 0 {
		alts, field = union.AnyOf, "anyOf"
	}

	// the whole of b decides the variant, since what tells them apart may be
	// declared in a common part too
	idx, err := matchingAlternative(alts, b)
	if err != nil {
		return true, &errpath.ErrField{Field: "allOf", Err: &errpath.ErrIndex{
			Index: unionIdx, Err: &errpath.ErrField{Field: field, Err: err},
		}}
	}

	rest := &openapi.Schema{
		Type:       openapi.TypeObject,
		Properties: propertiesOf(b, func(name string) bool { return !declared[name] }),
	}
	rest.Required = requiredAmong(b, rest.Properties)

	if err := mergeIntoAlternative(deref(alts[idx]), rest); err != nil {
		return true, &errpath.ErrField{Field: "allOf", Err: &errpath.ErrIndex{
			Index: unionIdx, Err: &errpath.ErrField{Field: field, Err: &errpath.ErrIndex{Index: idx, Err: err}},
		}}
	}

	return true, nil
}

// requiredAmong is what b requires of props, a part of b's properties, so that a part merged with them is narrowed
// only by what b leaves out.
func requiredAmong(b *openapi.Schema, props openapi.Schemas) []string {
	var out []string

	for name := range requiredOf(b) {
		if _, ok := props[name]; ok {
			out = append(out, name)
		}
	}

	slices.Sort(out)

	return out
}

// propertiesOf is b's properties that keep accepts, in b's order.
func propertiesOf(b *openapi.Schema, keep func(name string) bool) openapi.Schemas {
	props := openapi.Schemas{}

	for name, p := range b.Properties.ByIndex() {
		if keep(name) {
			props.Set(name, p)
		}
	}

	return props
}

// oneOfBranchMatches reports whether b could be an instance of the given oneOf alternative. A string must have
// alt's format, unless anyFormat lets an alternative without one take a string of any format.
func oneOfBranchMatches(alt, b *openapi.Schema, anyFormat bool) bool {
	// an integer is a number too
	if tp, err := effectiveType(alt); err != nil || tp != b.Type && (tp != openapi.TypeNumber || b.Type != openapi.TypeInteger) {
		return false
	}

	switch alt.Type {
	case openapi.TypeString:
		return alt.Format == b.Format || anyFormat && alt.Format == ""
	case openapi.TypeArray:
		// a list or a tuple only where the alternative has that shape, or merging it would nest another union
		return len(alt.PrefixItems) == len(b.PrefixItems) ||
			len(b.PrefixItems) == 0 && fitsEveryPosition(alt.PrefixItems, b.Items)
	default:
		return true
	}
}

// isDateAndDateTime reports whether one of a and b is a date string and the other a date-time string.
func isDateAndDateTime(a, b *openapi.Schema) bool {
	return a.Type == openapi.TypeString && b.Type == openapi.TypeString &&
		(a.Format == openapi.FormatDate && b.Format == openapi.FormatDateTime ||
			a.Format == openapi.FormatDateTime && b.Format == openapi.FormatDate)
}

// mergeDateOrDateTime merges a value seen as a date in one sample and as a date-time in another, such as Notion's
// date start, into a oneOf of the two, the date first, storing the result in both a and b.
func mergeDateOrDateTime(a, b *openapi.Schema) {
	date, dateTime := a, b
	if a.Format == openapi.FormatDateTime {
		date, dateTime = b, a
	}

	dateCopy, dateTimeCopy := *date, *dateTime
	dateCopy.Title, dateCopy.Description = "", ""
	dateTimeCopy.Title, dateTimeCopy.Description = "", ""

	merged := openapi.Schema{
		Title:       a.Title,
		Description: a.Description,
		OneOf:       openapi.SchemaList{&dateCopy, &dateTimeCopy},
	}

	a.Replace(&merged)
	b.Replace(&merged)
}

// mergeDateTimeOrTimestamp merges a value that appears as a date-time string in
// one sample and as a unix timestamp integer in another into a oneOf of the two,
// storing the result in both a and b.
func mergeDateTimeOrTimestamp(a, b *openapi.Schema) {
	strSchema, intSchema := a, b
	if a.Type == openapi.TypeInteger {
		strSchema, intSchema = b, a
	}

	strCopy, intCopy := *strSchema, *intSchema
	strCopy.Title, strCopy.Description = "", ""
	intCopy.Title, intCopy.Description = "", ""

	merged := openapi.Schema{
		Title:       a.Title,
		Description: a.Description,
		OneOf: openapi.SchemaList{
			&strCopy,
			&intCopy,
		},
	}

	a.Replace(&merged)
	b.Replace(&merged)
}

// isNumericString reports whether the string schema s holds numbers, as its example says: "9.5", say, not "foo".
func isNumericString(s *openapi.Schema) bool {
	var v string
	if json.Unmarshal(s.Example, &v) != nil {
		return false
	}

	f, err := strconv.ParseFloat(v, 64)

	return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) // "Infinity" is an overflow, see [isInfinityExample]
}

// mergeStringOrNumber makes a and b a oneOf of a string and a number, the string first, each with its own example: a
// number sent as a string sometimes and as a number other times. An integer's alternative is a number, as the strings
// may hold fractions.
func mergeStringOrNumber(a, b *openapi.Schema) {
	str, num := *a, *b
	if a.Type != openapi.TypeString {
		str, num = *b, *a
	}

	str.Title, str.Description = "", ""
	num.Title, num.Description = "", ""

	if num.Type == openapi.TypeInteger {
		num.Type, num.Format = openapi.TypeNumber, openapi.FormatDouble
	}

	merged := openapi.Schema{
		Title:       a.Title,
		Description: a.Description,
		OneOf:       openapi.SchemaList{&str, &num},
	}

	a.Replace(&merged)
	b.Replace(&merged)
}

// deref is the schema s stands for: the one it refers to, if it is a reference, following a reference to a
// reference too.
func deref(s *openapi.Schema) *openapi.Schema {
	// a bound, not a record of where it has been, stops a cycle of references: deref is called for every schema
	for range 64 {
		if s == nil || s.Ref == nil || s.Ref.Value == nil {
			break
		}

		s = s.Ref.Value
	}

	return s
}
