package ir

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// Tagged is set for a struct made from a tagged union whose alternatives differ in the value of their tag and in
// the members of their own, as Notion's blocks do: {"type": "paragraph", "paragraph": {...}}. The struct holds the
// members all alternatives share, the tag, and one field per member of an alternative's own; its methods check that
// only the members of the alternative the tag names are set.
type Tagged struct {
	// Tag is the tag's JSON name, Field its Go field and Type that field's Go type.
	Tag   string `json:"tag,omitzero"`
	Field string `json:"field,omitzero"`
	Type  string `json:"type,omitzero"`
	// Optional is set if an alternative may leave the tag out, which decoding then infers from the members set.
	Optional bool `json:"optional,omitzero"`
	// Enum is set if Type is an enum type of the tag's values that the struct declares itself.
	Enum []EnumValue `json:"enum,omitempty"`
	// Values are the alternatives, one per value of the tag.
	Values []TaggedValue `json:"values,omitempty"`
	// Members are the fields of the alternatives' own members, each once.
	Members []TaggedMember `json:"members,omitempty"`
}

// TaggedValue is one alternative of a [Tagged] struct: its value of the tag and the members of its own, by JSON name.
type TaggedValue struct {
	Value   string      `json:"value,omitzero"`
	Members []TaggedOwn `json:"members,omitempty"`
}

// TaggedOwn is a member of an alternative's own, by JSON name, whether the alternative requires it, and whether it
// may be null, which leaves its field as if it were left out.
type TaggedOwn struct {
	Name     string `json:"name,omitzero"`
	Required bool   `json:"required,omitzero"`
	Nullable bool   `json:"nullable,omitzero"`
}

// Need is how the alternative needs the member, by the name of the generated constant.
func (o TaggedOwn) Need() string {
	switch {
	case !o.Required:
		return "jsonTagOptional"
	case o.Nullable:
		return "jsonTagRequiredOrNull"
	default:
		return "jsonTagRequired"
	}
}

// TaggedMember is the field of a member of an alternative's own.
type TaggedMember struct {
	// Name is the member's JSON name, Field its Go field, and Zero what the field holds while it is not set.
	Name  string `json:"name,omitzero"`
	Field string `json:"field,omitzero"`
	Zero  string `json:"zero,omitzero"`
}

// namedSchema is a property and its name.
type namedSchema struct {
	name   string
	schema *openapi.Schema
}

// orderedProperties returns the properties s declares, following references and allOf, in the order declared.
func orderedProperties(s *openapi.Schema) []namedSchema {
	s = deref(s)
	if s == nil {
		return nil
	}

	var out []namedSchema

	seen := map[string]bool{}
	add := func(l []namedSchema) {
		for _, p := range l {
			if !seen[p.name] {
				seen[p.name] = true
				out = append(out, p)
			}
		}
	}

	for name, p := range s.Properties.ByIndex() {
		add([]namedSchema{{name, p}})
	}

	for _, e := range s.AllOf {
		add(orderedProperties(e))
	}

	return out
}

// taggedShape is what makes the alternatives of a union a [Tagged] struct.
type taggedShape struct {
	tag string
	// tagOptional is set if an alternative does not require the tag.
	tagOptional bool
	values      []string
	// shared are the members every alternative has, but the tag, in the order the first declares them.
	shared []namedSchema
	// sharedRequired are the shared members every alternative requires.
	sharedRequired map[string]bool
	// own are each alternative's members of its own, and ownRequired those of them it requires.
	own         [][]namedSchema
	ownRequired []map[string]bool
	// leaves are the alternatives, those of a nested union in its place, and parents the nested union of each, if any.
	leaves, parents openapi.SchemaList
}

// unionLeaves returns the alternatives alts, each of those that is a union replaced by its own, however deep, and the
// union each came from, nil for one of alts.
func unionLeaves(alts openapi.SchemaList, parent *openapi.Schema, depth int) (leaves, parents openapi.SchemaList) {
	for _, a := range alts {
		if d := deref(a); depth < 8 && d != nil && d.Type == "" && (len(d.OneOf) > 0 || len(d.AnyOf) > 0) &&
			!collapsesOneOf(d) && nullableVariant(d) == nil {
			l, p := unionLeaves(alternatives(d), a, depth+1)
			leaves, parents = append(leaves, l...), append(parents, p...)

			continue
		}

		leaves, parents = append(leaves, a), append(parents, parent)
	}

	return leaves, parents
}

// taggedUnion returns the shape of the union u with the alternatives alts, if it can be a [Tagged] struct: every
// alternative, or alternative of a nested union, is an object with a member, the tag, pinned to a string of its own,
// and has no other member but those all of them have, the same in each, and its own: at most one, named after its
// value, or more beside the one named after its value. A member of its own several alternatives have is the same in
// each.
func taggedUnion(u *openapi.Schema, alts openapi.SchemaList) (*taggedShape, bool) {
	leaves, parents := unionLeaves(alts, nil, 0)

	// a discriminator's mapping names the alternatives, not their leaves
	if len(leaves) != len(alts) {
		u = &openapi.Schema{}
	}

	alts = leaves

	if len(alts) < 2 || slices.ContainsFunc(alts, isNull) {
		return nil, false
	}

	for _, a := range alts {
		if _, _, ok := objectShape(a); !ok {
			return nil, false
		}
	}

	tag, values, ok := discriminate(u, alts)
	if !ok {
		return nil, false
	}

	shape := &taggedShape{
		tag:            tag,
		values:         values,
		sharedRequired: map[string]bool{},
		own:            make([][]namedSchema, len(alts)),
		ownRequired:    make([]map[string]bool, len(alts)),
		leaves:         leaves,
		parents:        parents,
	}

	props := make([][]namedSchema, len(alts))
	required := make([][]string, len(alts))

	for i, a := range alts {
		props[i] = orderedProperties(a)
		_, required[i], _ = objectShape(a)
		shape.tagOptional = shape.tagOptional || !slices.Contains(required[i], tag)
	}

	has := func(i int, name string) *openapi.Schema {
		for _, p := range props[i] {
			if p.name == name {
				return p.schema
			}
		}

		return nil
	}

	for _, p := range props[0] {
		if p.name == tag || p.name == values[0] && !sharedBy(alts, p.name) {
			continue
		}

		if !sharedBy(alts, p.name) {
			return nil, false
		}

		for i := range alts {
			if !sameSchema(has(i, p.name), p.schema) {
				return nil, false
			}
		}

		shape.shared = append(shape.shared, p)
		shape.sharedRequired[p.name] = !slices.ContainsFunc(required, func(r []string) bool {
			return !slices.Contains(r, p.name)
		})
	}

	byName := map[string]*openapi.Schema{}

	for i, value := range values {
		shape.ownRequired[i] = map[string]bool{}

		for _, p := range props[i] {
			if p.name == tag || slices.ContainsFunc(shape.shared, func(s namedSchema) bool { return s.name == p.name }) {
				continue
			}

			if prev, ok := byName[p.name]; ok && !sameSchema(prev, p.schema) {
				return nil, false
			}

			byName[p.name] = p.schema
			shape.own[i] = append(shape.own[i], p)
			shape.ownRequired[i][p.name] = slices.Contains(required[i], p.name)
		}

		// members beyond the one named after the value are the alternative's only beside it
		if own := shape.own[i]; len(own) > 0 && !slices.ContainsFunc(own, func(p namedSchema) bool { return p.name == value }) {
			return nil, false
		}
	}

	return shape, true
}

// sharedBy reports whether every alternative declares the member name.
func sharedBy(alts openapi.SchemaList, name string) bool {
	return !slices.ContainsFunc(alts, func(a *openapi.Schema) bool { return propertyOf(a, name) == nil })
}

// sameSchema reports whether a and b describe the same values, whatever they say about them.
func sameSchema(a, b *openapi.Schema) bool {
	if a == nil || b == nil {
		return a == b
	}

	if a.Ref != nil || b.Ref != nil {
		return a.Ref != nil && b.Ref != nil && a.Ref.Identifier == b.Ref.Identifier
	}

	strip := func(s *openapi.Schema) string {
		c := *s
		c.Title, c.Description, c.Example, c.Examples, c.Extensions = "", "", nil, nil, nil

		data, err := json.Marshal(&c, json.Deterministic(true))
		if err != nil {
			return fmt.Sprintf("%p", s) // never the same as another
		}

		return string(data)
	}

	return strip(a) == strip(b)
}

// isTaggedUnion reports whether s is a union rendered as a [Tagged] struct.
func isTaggedUnion(s *openapi.Schema) bool {
	s = deref(s)
	if s == nil || s.Type != "" || len(s.OneOf) == 0 && len(s.AnyOf) == 0 {
		return false
	}

	_, ok := taggedUnion(s, alternatives(s))

	return ok
}

// taggedShapeOf returns the members of the [Tagged] struct of the union u and those it requires: those every
// alternative does, of its shared ones and its tag. ok is false if u is no such union.
func taggedShapeOf(u *openapi.Schema) (members, required []string, ok bool) {
	shape, ok := taggedUnion(u, alternatives(u))
	if !ok {
		return nil, nil, false
	}

	// what it requires is what every alternative does: the tag too, unless one leaves it out
	members = []string{shape.tag}
	if !shape.tagOptional {
		required = []string{shape.tag}
	}

	for _, p := range shape.shared {
		members = append(members, p.name)
		if shape.sharedRequired[p.name] {
			required = append(required, p.name)
		}
	}

	for _, own := range shape.own {
		for _, p := range own {
			members = append(members, p.name)
		}
	}

	slices.Sort(members)
	slices.Sort(required)

	return slices.Compact(members), required, true
}

// taggedFields returns the fields of the [Tagged] struct of shape, and the struct's Tagged. The fields of members
// existing declares, by JSON name, are left out: it already has them. ok is false if the fields cannot be written,
// such as for two members whose Go names are the same.
func taggedFields(shape *taggedShape, existing []Field) ([]Field, *Tagged, bool, error) {
	var fields []Field

	names := map[string]bool{}
	byJSON := map[string]Field{}

	for _, f := range existing {
		names[f.Name] = true
		if f.JSONName != "" {
			byJSON[f.JSONName] = f
		}
	}

	add := func(f Field) bool {
		if names[f.Name] {
			return false
		}

		names[f.Name] = true
		fields = append(fields, f)

		return true
	}

	tagged := &Tagged{Tag: shape.tag, Optional: shape.tagOptional}

	if f, ok := byJSON[shape.tag]; ok {
		if f.Type[0] == '*' {
			return nil, nil, false, nil
		}

		tagged.Field, tagged.Type = f.Name, f.Type
	} else {
		tagged.Field, tagged.Type = fieldGoName(shape.tag), "string"
	}

	for _, p := range shape.shared {
		if _, ok := byJSON[p.name]; ok {
			continue
		}

		f, err := getField(p.name, p.schema, shape.sharedRequired)
		if err != nil {
			return nil, nil, false, fmt.Errorf("property %q: %w", p.name, err)
		}

		if !add(f) {
			return nil, nil, false, nil
		}
	}

	if _, ok := byJSON[shape.tag]; !ok {
		if !add(Field{
			Name:     tagged.Field,
			JSONName: shape.tag,
			Type:     tagged.Type,
			JSONTag:  buildJSONTag(shape.tag, !shape.tagOptional),
			Required: !shape.tagOptional,
		}) {
			return nil, nil, false, nil
		}
	}

	declared := map[string]bool{}

	for i, value := range shape.values {
		tv := TaggedValue{Value: value}

		for _, p := range shape.own[i] {
			own := p.schema

			tp, err := SchemaGoType(own)
			if err != nil {
				return nil, nil, false, fmt.Errorf("property %q: %w", p.name, err)
			}

			nullable := tp.IsPointer || nullableVariant(deref(own)) != nil
			tv.Members = append(tv.Members, TaggedOwn{Name: p.name, Required: shape.ownRequired[i][p.name], Nullable: nullable})

			if declared[p.name] {
				continue
			}

			if _, ok := byJSON[p.name]; ok {
				return nil, nil, false, nil
			}

			declared[p.name] = true

			zero := unsetValue(own, tp)
			fieldType := tp.String()

			switch {
			case tp.IsPointer:
				zero = "nil"
			case zero == "":
				fieldType, zero = "*"+fieldType, "nil"
			}

			m := TaggedMember{Name: p.name, Field: fieldGoName(p.name), Zero: zero}
			tagged.Members = append(tagged.Members, m)

			if !add(Field{
				Name:        m.Field,
				JSONName:    p.name,
				Type:        fieldType,
				JSONTag:     buildJSONTag(p.name, false),
				Description: cmp.Or(own.Description, deref(own).Description),
			}) {
				return nil, nil, false, nil
			}
		}

		slices.SortFunc(tv.Members, func(a, b TaggedOwn) int { return strings.Compare(a.Name, b.Name) })
		tagged.Values = append(tagged.Values, tv)
	}

	return fields, tagged, true, nil
}

// fromTaggedUnion builds the struct of the union u if it can be a [Tagged] struct. Alternatives only it refers to are
// marked as folded into it, as they need no type of their own.
func fromTaggedUnion(name string, u *openapi.Schema, uses map[string]int, folded map[string]bool) (*Schema, error) {
	alts := alternatives(u)

	shape, ok := taggedUnion(u, alts)
	if !ok {
		return nil, nil
	}

	fields, tagged, ok, err := taggedFields(shape, nil)
	if err != nil || !ok {
		return nil, err
	}

	foldAlternatives(shape, uses, folded)

	return &Schema{
		Name:        name,
		Description: getDescription(u, name),
		Kind:        SchemaKindStruct,
		Fields:      fields,
		Tagged:      tagged,
	}, nil
}

// foldAlternatives marks the alternatives of a [Tagged] struct that nothing else refers to as folded into it: nested
// unions, and leaves whose union needs no type of their own either.
func foldAlternatives(shape *taggedShape, uses map[string]int, folded map[string]bool) {
	once := func(s *openapi.Schema) bool { return s.Ref != nil && uses[s.Ref.Identifier] == 1 }

	for i, leaf := range shape.leaves {
		parent := shape.parents[i]
		if parent != nil && once(parent) {
			folded[parent.Ref.Identifier] = true
		}

		if once(leaf) && (parent == nil || once(parent) || isTaggedUnion(parent)) {
			folded[leaf.Ref.Identifier] = true
		}
	}
}

// typeTags gives each [Tagged] struct whose tag is a plain string an enum type of the tag's values, named after the
// struct and the tag, such as BlockType with BlockTypeParagraph. A tag keeps string where a name is taken.
func typeTags(schemas []Schema) {
	taken := map[string]bool{}

	for _, s := range schemas {
		taken[s.Name] = true
		for _, v := range s.EnumValues {
			taken[v.GoName] = true
		}
	}

	for i := range schemas {
		t := schemas[i].Tagged
		if t == nil || t.Type != "string" {
			continue
		}

		name := schemas[i].Name + t.Field
		names := []string{name}
		enum := make([]EnumValue, len(t.Values))

		for j, v := range t.Values {
			enum[j] = EnumValue{GoName: enumConstName(name, v.Value), Value: v.Value, Literal: strconv.Quote(v.Value)}
			names = append(names, enum[j].GoName)
		}

		if slices.ContainsFunc(names, func(n string) bool { return taken[n] }) || !distinct(names) {
			continue
		}

		for _, n := range names {
			taken[n] = true
		}

		t.Type, t.Enum = name, enum

		for j, f := range schemas[i].Fields {
			if f.Name == t.Field && f.JSONName == t.Tag {
				schemas[i].Fields[j].Type = name
			}
		}
	}
}
