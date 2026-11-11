package ir

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// UnionRoundTrip is a union a generated test sets each variant of in turn, encodes and decodes strictly: the variant
// must come back, encoding the same. That tests what the union's own methods do, as which alternative its decoder
// chooses, by a discriminator, by the members an object has, or by the kind of a value.
type UnionRoundTrip struct {
	Union    string             `json:"union,omitzero"`
	Variants []RoundTripVariant `json:"variants,omitempty"`
}

// RoundTripVariant is one variant of a [UnionRoundTrip]: a literal of it, and the field of the union that holds it.
type RoundTripVariant struct {
	FieldName string `json:"fieldName,omitzero"`
	Value     string `json:"value,omitzero"`
	// ByValue is set where the field holds the variant itself, unset while it holds Unset, rather than a pointer.
	ByValue bool   `json:"byValue,omitzero"`
	Unset   string `json:"unset,omitzero"`
}

// UnionRoundTrips are the unions a generated test round-trips, each with the variants a value can be built of from
// the specification alone, and that no other variant of the union would decode as too. A value sets every union it
// holds to an alternative that union tells apart, every tagged struct to a tag and what that tag requires, and every
// date to a date: see [Document.valueLiteral].
func (doc Document) UnionRoundTrips() []UnionRoundTrip {
	var out []UnionRoundTrip

	for _, s := range doc.Schemas {
		if s.Kind != SchemaKindUnion || s.Tagged != nil {
			continue
		}

		rt := UnionRoundTrip{Union: s.Name}

		for i, v := range s.UnionVariants {
			if s.Discriminator == "" && doc.overlaps(s, i) {
				continue
			}

			value, ok := doc.variantLiteral(s, v, map[string]bool{})
			if !ok {
				continue
			}

			rv := RoundTripVariant{FieldName: v.FieldName, Value: value, Unset: "nil"}
			if v.Zero != "" {
				rv.ByValue, rv.Unset = true, v.Zero
			}

			rt.Variants = append(rt.Variants, rv)
		}

		if len(rt.Variants) > 0 {
			out = append(out, rt)
		}
	}

	return out
}

// TaggedRoundTrip is a struct told apart by a tag a generated test sets each value of in turn, with the members that
// value requires, encodes and decodes strictly: the tag must come back, and the struct encode the same.
type TaggedRoundTrip struct {
	Type   string            `json:"type,omitzero"`
	Field  string            `json:"field,omitzero"`
	Values []TaggedRoundTrip `json:"values,omitempty"`
	// Value and Literal are set on an entry of Values: the tag's value, and a literal of the struct holding it.
	Value   string `json:"value,omitzero"`
	Literal string `json:"literal,omitzero"`
}

// TaggedRoundTrips are the tagged structs a generated test round-trips, each with the tag values a literal can be
// built of.
func (doc Document) TaggedRoundTrips() []TaggedRoundTrip {
	var out []TaggedRoundTrip

	for _, s := range doc.Schemas {
		if s.Tagged == nil {
			continue
		}

		rt := TaggedRoundTrip{Type: s.Name, Field: s.Tagged.Field}

		for i, v := range s.Tagged.Values {
			if lit, ok := doc.taggedLiteral(s, i, map[string]bool{}); ok {
				rt.Values = append(rt.Values, TaggedRoundTrip{Value: v.Value, Literal: lit})
			}
		}

		if len(rt.Values) > 0 {
			out = append(out, rt)
		}
	}

	return out
}

// literalPart is a field of a composite literal and its value.
type literalPart struct{ name, value string }

func renderLiteral(goType string, parts []literalPart) string {
	values := make([]string, len(parts))
	for i, p := range parts {
		values[i] = p.name + ": " + p.value
	}

	return goType + "{" + strings.Join(values, ", ") + "}"
}

// setPart sets the field name to value among parts, in its place if it has one.
func setPart(parts []literalPart, name, value string) []literalPart {
	if i := slices.IndexFunc(parts, func(p literalPart) bool { return p.name == name }); i >= 0 {
		parts[i].value = value
		return parts
	}

	return append(parts, literalPart{name, value})
}

// valueLiteral is a Go literal of goType that a strict decoder reads back as it was, or "" where its zero value is
// one. It is [Document.MinimalLiteral], but for what a minimal value does not decode as itself: a union it holds is
// set to an alternative the union tells apart, a tagged struct to its first tag and what that requires, and a date to
// a date, as the zero one, 0000-00-00, is none. ok is false where no such literal can be built.
func (doc Document) valueLiteral(goType string, seen map[string]bool) (lit string, ok bool) {
	if goType == "civil.Date" {
		return "civil.Date{Year: 2026, Month: 1, Day: 2}", true
	}

	s, found := doc.schemaNamed(goType)
	if !found || seen[goType] {
		return "", true
	}

	seen[goType] = true
	defer delete(seen, goType)

	switch {
	case s.Tagged != nil:
		return doc.taggedLiteral(s, 0, seen)
	case s.Kind == SchemaKindUnion:
		for i, v := range s.UnionVariants {
			if s.Discriminator == "" && doc.overlaps(s, i) {
				continue
			}

			if lit, ok := doc.variantLiteral(s, v, seen); ok {
				if v.Zero == "" {
					lit = "new(" + lit + ")"
				}

				return renderLiteral(goType, []literalPart{{v.FieldName, lit}}), true
			}
		}

		return "", false
	case s.Kind == SchemaKindStruct || s.Kind == SchemaKindAllOf:
		parts, ok := doc.structParts(s, seen)
		if !ok || len(parts) == 0 {
			return "", ok
		}

		return renderLiteral(goType, parts), true
	default:
		return "", true
	}
}

// structParts are the fields of a literal of the struct s a strict decoder reads back as it was: its required fields,
// a fixed one at the value it allows, and the parts it embeds, but where their zero value is one.
func (doc Document) structParts(s Schema, seen map[string]bool) ([]literalPart, bool) {
	var parts []literalPart

	for _, f := range s.Fields {
		if f.Fixed != "" && f.Required {
			// a const or one-value enum: the one value it decodes as
			value := f.Fixed
			if base, ptr := strings.CutPrefix(f.Type, "*"); ptr {
				value = "new(" + base + "(" + value + "))"
			}

			parts = append(parts, literalPart{f.Name, value})

			continue
		}

		if strings.HasPrefix(f.Type, "*") || !f.Required && !f.Embedded {
			continue
		}

		lit, ok := doc.valueLiteral(f.Type, seen)
		if !ok {
			return nil, false
		}

		if lit == "" {
			continue
		}

		name := f.Name
		if f.Embedded {
			name = f.Type
		}

		parts = append(parts, literalPart{name, lit})
	}

	return parts, true
}

// taggedLiteral is a literal of the tagged struct s holding its i-th tag, with the members that tag requires set.
func (doc Document) taggedLiteral(s Schema, i int, seen map[string]bool) (string, bool) {
	t := s.Tagged
	v := t.Values[i]

	parts, ok := doc.structParts(s, seen)
	if !ok {
		return "", false
	}

	parts = setPart(parts, t.Field, strconv.Quote(v.Value))

	for _, m := range t.Members {
		if !slices.ContainsFunc(v.Members, func(o TaggedOwn) bool { return o.Name == m.Name && o.Required }) {
			continue
		}

		j := slices.IndexFunc(s.Fields, func(f Field) bool { return f.Name == m.Field })
		if j < 0 {
			return "", false
		}

		lit, ok := doc.setValue(s.Fields[j].Type, seen)
		if !ok {
			return "", false
		}

		parts = setPart(parts, m.Field, lit)
	}

	return renderLiteral(s.Name, parts), true
}

// setValue is a literal of a field of goType, a pointer or not, that reads as set: not its zero value.
func (doc Document) setValue(goType string, seen map[string]bool) (string, bool) {
	base, ptr := strings.CutPrefix(goType, "*")

	lit, ok := doc.valueLiteral(base, seen)
	if !ok {
		return "", false
	}

	if lit == "" {
		if sample, ok := doc.sampleLiteral(base); ok {
			lit = sample
		} else {
			lit = base + "{}"
		}
	}

	if ptr {
		return "new(" + lit + ")", true
	}

	return lit, true
}

// variantLiteral is a literal of variant v of union s, with what tells it apart set: its pinned members, the
// discriminator, and a value of each enum it holds. ok is false where no such literal can be built.
func (doc Document) variantLiteral(s Schema, v UnionVariant, seen map[string]bool) (string, bool) {
	if lit, ok := doc.sampleLiteral(v.Type); ok {
		return lit, true
	}

	vs, found := doc.schemaNamed(v.Type)
	switch {
	case !found:
		return "", false
	case vs.Kind == SchemaKindUnion || vs.Tagged != nil:
		lit, ok := doc.valueLiteral(v.Type, seen)
		return lit, ok && lit != ""
	case vs.Kind != SchemaKindStruct:
		return "", false
	}

	parts, ok := doc.structParts(vs, seen)
	if !ok {
		return "", false
	}

	fields := map[string]Field{}
	for _, f := range vs.Fields {
		if f.JSONName != "" && !f.Embedded {
			fields[f.JSONName] = f
		}
	}

	set := func(member, jsonValue string) bool {
		f, ok := fields[member]
		if !ok {
			return false
		}

		if base, ptr := strings.CutPrefix(f.Type, "*"); ptr {
			jsonValue = "new(" + base + "(" + jsonValue + "))"
		}

		parts = setPart(parts, f.Name, jsonValue)

		return true
	}

	pinned := map[string]bool{}
	for _, p := range v.Pinned {
		if pinned[p.Name] = true; !set(p.Name, p.Value) {
			return "", false
		}
	}

	if s.Discriminator != "" && v.Value != "" && !pinned[s.Discriminator] && !set(s.Discriminator, strconv.Quote(v.Value)) {
		return "", false
	}

	for _, e := range v.Enums {
		if !pinned[e.Name] && len(e.Values) > 0 && !set(e.Name, e.Values[0]) {
			return "", false
		}
	}

	return renderLiteral(v.Type, parts), true
}

// sampleLiteral is a literal of goType other than its zero value, which a union reads as no variant set, where goType
// is a scalar, an enum, or a slice of something a value can be built of.
func (doc Document) sampleLiteral(goType string) (string, bool) {
	switch goType {
	case "string":
		return `"sample"`, true
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		return "1", true
	case "float32", "float64":
		return "1.5", true
	case "bool":
		return "true", true
	case "civil.Date":
		return "civil.Date{Year: 2026, Month: 1, Day: 2}", true
	case "time.Time":
		return "time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)", true
	case "uuid.UUID":
		return `uuid.MustParse("6344e926-01ed-4659-81e8-ea3174a9c351")`, true
	case "url.URL":
		return `url.URL{Scheme: "https", Host: "example.com", Path: "/sample"}`, true
	}

	if elem, ok := strings.CutPrefix(goType, "[]"); ok {
		if lit, ok := doc.elemLiteral(elem); ok {
			return goType + "{" + lit + "}", true
		}

		return "", false
	}

	s, ok := doc.schemaNamed(goType)
	if !ok {
		return "", false
	}

	switch {
	case s.Kind == SchemaKindEnum && len(s.EnumValues) > 0:
		return s.EnumValues[0].GoName, true
	case s.Kind == SchemaKindAlias:
		if elem, ok := strings.CutPrefix(s.Type, "[]"); ok {
			if lit, ok := doc.elemLiteral(elem); ok {
				return goType + "{" + lit + "}", true
			}

			return "", false
		}

		if lit, ok := doc.sampleLiteral(s.Type); ok && !strings.HasPrefix(s.Type, "map[") {
			return goType + "(" + lit + ")", true
		}
	}

	return "", false
}

// elemLiteral is a literal of an element of a slice: a sample of a scalar, or a value of a struct or union.
func (doc Document) elemLiteral(goType string) (string, bool) {
	if lit, ok := doc.sampleLiteral(goType); ok {
		return lit, true
	}

	s, ok := doc.schemaNamed(goType)
	if !ok || s.Kind != SchemaKindStruct && s.Kind != SchemaKindUnion && s.Tagged == nil {
		return "", false
	}

	lit, ok := doc.valueLiteral(goType, map[string]bool{})
	if lit == "" {
		lit = goType + "{}"
	}

	return lit, ok
}

// overlaps reports whether a minimal value of the i-th variant of s, a union told apart by what its value holds, could
// be decoded as another variant too: one of the same kind of JSON value, or, for objects, one whose required members
// it holds and whose members are all it holds.
func (doc Document) overlaps(s Schema, i int) bool {
	v := s.UnionVariants[i]
	kind := doc.jsonKind(v.Type)

	for j, w := range s.UnionVariants {
		if j == i || doc.jsonKind(w.Type) != kind {
			continue
		}

		if kind != "object" || !v.Object || !w.Object {
			return true
		}

		// what a minimal value of v holds: its required members, the pinned ones among them
		holds := map[string]bool{}
		for _, m := range v.Required {
			holds[m] = true
		}

		pins := map[string]string{}
		for _, p := range v.Pinned {
			holds[p.Name], pins[p.Name] = true, p.Value
		}

		fits := !slices.ContainsFunc(w.Required, func(m string) bool { return !holds[m] }) &&
			!slices.ContainsFunc(slices.Collect(maps.Keys(holds)), func(m string) bool { return !slices.Contains(w.Members, m) }) &&
			!slices.ContainsFunc(w.Pinned, func(p PinnedMember) bool { return holds[p.Name] && pins[p.Name] != p.Value })
		if fits {
			return true
		}
	}

	return false
}

// jsonKind is the kind of JSON value a value of goType is written as: string, number, bool, array or object.
func (doc Document) jsonKind(goType string) string {
	goType = strings.TrimPrefix(goType, "*")

	switch {
	case strings.HasPrefix(goType, "[]"):
		return "array"
	case strings.HasPrefix(goType, "map["):
		return "object"
	}

	switch goType {
	case "string", "uuid.UUID", "time.Time", "civil.Date", "url.URL", "types.Email", "net.IP":
		return "string"
	case "int", "int32", "int64", "uint", "uint32", "uint64", "float32", "float64":
		return "number"
	case "bool":
		return "bool"
	}

	s, ok := doc.schemaNamed(goType)
	switch {
	case !ok:
		return goType
	case s.Kind == SchemaKindEnum || s.Kind == SchemaKindAlias:
		return doc.jsonKind(s.Type)
	case s.Kind == SchemaKindTuple:
		return "array"
	default:
		return "object"
	}
}

// schemaNamed is the schema of the Go type name, if the package declares it.
func (doc Document) schemaNamed(name string) (Schema, bool) {
	i := slices.IndexFunc(doc.Schemas, func(s Schema) bool { return s.Name == name })
	if i < 0 {
		return Schema{}, false
	}

	return doc.Schemas[i], true
}
