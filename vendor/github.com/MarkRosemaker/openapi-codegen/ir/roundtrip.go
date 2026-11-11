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

// RoundTripVariant is one variant of a [UnionRoundTrip]: a value v of it, the statements that set on v what tells the
// variant apart, and the field of the union that holds it.
type RoundTripVariant struct {
	FieldName string   `json:"fieldName,omitzero"`
	Value     string   `json:"value,omitzero"`
	Setup     []string `json:"setup,omitempty"`
	// ByValue is set where the field holds the variant itself, unset while it holds Unset, rather than a pointer.
	ByValue bool   `json:"byValue,omitzero"`
	Unset   string `json:"unset,omitzero"`
}

// UnionRoundTrips are the unions a generated test round-trips, each with the variants a value can be built of from
// the specification alone, and that no other variant of the union would decode as too. Left out are a variant that
// needs a union of its own, whose alternative a minimal value does not tell apart, and one holding a date, whose zero
// value is no date.
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

			value, setup, ok := doc.variantValue(s, v)
			if !ok {
				continue
			}

			rv := RoundTripVariant{FieldName: v.FieldName, Value: value, Setup: setup, Unset: "nil"}
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

// variantValue is a literal of variant v of union s, and the statements on it, named v, that set what tells it apart:
// its pinned members, the discriminator, and a value of each enum it holds. ok is false where no such value can be
// built.
func (doc Document) variantValue(s Schema, v UnionVariant) (value string, setup []string, ok bool) {
	if doc.requiresUnion(v.Type, map[string]bool{}) || doc.requiresDate(v.Type, map[string]bool{}) {
		return "", nil, false
	}

	if lit, ok := doc.sampleLiteral(v.Type); ok {
		return lit, nil, true
	}

	vs, found := doc.schemaNamed(v.Type)
	if !found || vs.Kind != SchemaKindStruct {
		return "", nil, false
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
			setup = append(setup, "v."+f.Name+" = new("+base+"("+jsonValue+"))")
		} else {
			setup = append(setup, "v."+f.Name+" = "+jsonValue)
		}

		return true
	}

	pinned := map[string]bool{}
	for _, p := range v.Pinned {
		if pinned[p.Name] = true; !set(p.Name, p.Value) {
			return "", nil, false
		}
	}

	if s.Discriminator != "" && v.Value != "" && !pinned[s.Discriminator] && !set(s.Discriminator, strconv.Quote(v.Value)) {
		return "", nil, false
	}

	for _, e := range v.Enums {
		if !pinned[e.Name] && len(e.Values) > 0 && !set(e.Name, e.Values[0]) {
			return "", nil, false
		}
	}

	return doc.MinimalLiteral(v.Type), setup, true
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

// elemLiteral is a literal of an element of a slice: a sample of a scalar, or a struct's minimal literal.
func (doc Document) elemLiteral(goType string) (string, bool) {
	if lit, ok := doc.sampleLiteral(goType); ok {
		return lit, true
	}

	if s, ok := doc.schemaNamed(goType); ok && s.Kind == SchemaKindStruct {
		return doc.MinimalLiteral(goType), true
	}

	return "", false
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

// requiresDate reports whether a value of goType needs a civil.Date, whose zero value, 0000-00-00, decodes as no date.
func (doc Document) requiresDate(goType string, seen map[string]bool) bool {
	goType = strings.TrimPrefix(goType, "*")
	if goType == "civil.Date" {
		return true
	}

	if seen[goType] {
		return false
	}

	seen[goType] = true

	s, ok := doc.schemaNamed(goType)
	if !ok {
		return false
	}

	return slices.ContainsFunc(s.Fields, func(f Field) bool {
		return (f.Required || f.Embedded) && doc.requiresDate(f.Type, seen)
	})
}

// schemaNamed is the schema of the Go type name, if the package declares it.
func (doc Document) schemaNamed(name string) (Schema, bool) {
	i := slices.IndexFunc(doc.Schemas, func(s Schema) bool { return s.Name == name })
	if i < 0 {
		return Schema{}, false
	}

	return doc.Schemas[i], true
}
