package ir

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// objectShape returns the members an object schema declares and those it requires, following references and allOf.
// A union that is a [Tagged] struct is the struct's object. ok is false for any other schema that is not a plain
// object, such as a union.
func objectShape(s *openapi.Schema) (members, required []string, ok bool) {
	s = deref(s)
	if s != nil && s.Type == "" && (len(s.OneOf) > 0 || len(s.AnyOf) > 0) {
		return taggedShapeOf(s)
	}

	if s == nil || s.Type != "" && s.Type != openapi.TypeObject {
		return nil, nil, false
	}

	for name := range s.Properties.ByIndex() {
		members = append(members, name)
	}

	required = requiredOf(s)

	for _, e := range s.AllOf {
		m, r, ok := objectShape(e)
		if !ok {
			return nil, nil, false
		}

		members, required = append(members, m...), append(required, r...)
	}

	// a part may require what another declares one way
	required = slices.DeleteFunc(required, func(name string) bool {
		return slices.ContainsFunc(orderedProperties(s), func(p namedSchema) bool { return p.name == name && oneWay(p.schema) })
	})

	slices.Sort(members)
	slices.Sort(required)

	return slices.Compact(members), slices.Compact(required), true
}

// constString returns the string a schema allows as its only value, from const or a single enum value.
func constString(s *openapi.Schema) (string, bool) {
	s = deref(s)

	v := s.Const
	if len(v) == 0 && len(s.Enum) == 1 {
		v = s.Enum[0]
	}

	var str string
	if len(v) == 0 || json.Unmarshal(v, &str) != nil {
		return "", false
	}

	return str, true
}

// propertyOf returns the schema of the member name that s declares, following references and allOf.
func propertyOf(s *openapi.Schema, name string) *openapi.Schema {
	s = deref(s)
	if p, ok := s.Properties[name]; ok {
		return p
	}

	for _, e := range s.AllOf {
		if p := propertyOf(e, name); p != nil {
			return p
		}
	}

	return nil
}

// discriminate returns the member that tells a union's variants apart and each variant's value of it: the
// discriminator's propertyName, or a member every variant declares with a string const of its own. ok is false if
// there is no such member.
func discriminate(u *openapi.Schema, variants openapi.SchemaList) (member string, values []string, ok bool) {
	if d := u.Discriminator; d != nil {
		values = make([]string, len(variants))
		for i, v := range variants {
			if v.Ref == nil {
				return "", nil, false
			}

			// an explicit mapping wins over the component's name, its first entry if several name the variant
			values[i] = v.Ref.Identifier[strings.LastIndex(v.Ref.Identifier, "/")+1:]
			for key, target := range d.Mapping.ByIndex() {
				if openapi.MappingRef(target.Value) == v.Ref.Identifier {
					values[i] = key
					break
				}
			}
		}

		return d.PropertyName, values, distinct(values)
	}

	if len(variants) == 0 {
		return "", nil, false
	}

	first, _, ok := objectShape(variants[0])
	if !ok {
		return "", nil, false
	}

	for _, name := range first {
		values = values[:0]

		for _, v := range variants {
			p := propertyOf(v, name)
			if p == nil {
				break
			}

			value, ok := constString(p)
			if !ok {
				break
			}

			values = append(values, value)
		}

		if len(values) == len(variants) && distinct(values) {
			return name, values, true
		}
	}

	return "", nil, false
}

func distinct(values []string) bool {
	seen := make(map[string]bool, len(values))
	for _, v := range values {
		if seen[v] {
			return false
		}

		seen[v] = true
	}

	return true
}

// hasJSONMethods reports whether the Go type generated for s encodes itself, so that embedding it would hand its
// methods to the struct embedding it.
func hasJSONMethods(s *openapi.Schema) bool {
	s = deref(s)

	switch {
	case s == nil, collapsesOneOf(s), nullableVariant(s) != nil:
		return false
	case len(s.PrefixItems) > 0, s.Type == "" && (len(s.OneOf) > 0 || len(s.AnyOf) > 0):
		return true
	default:
		return slices.ContainsFunc(s.AllOf, hasJSONMethods)
	}
}

// isFoldable reports whether s is a plain object whose properties can be written as fields of the struct of an
// allOf it is part of.
func isFoldable(s *openapi.Schema) bool {
	s = deref(s)

	return s != nil && (s.Type == openapi.TypeObject || s.Type == "") && len(s.Properties) > 0 &&
		len(s.AllOf) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 && mapValues(s) == nil
}

// countSchemaUses counts the references to each component schema anywhere in doc, by reference identifier.
func countSchemaUses(doc *openapi.Document) (map[string]int, error) {
	data, err := doc.ToJSON()
	if err != nil {
		return nil, err
	}

	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}

	uses := map[string]int{}

	var walk func(any)

	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok && strings.HasPrefix(ref, "#/components/schemas/") {
				uses[ref]++
			}

			for _, e := range v {
				walk(e)
			}
		case []any:
			for _, e := range v {
				walk(e)
			}
		}
	}

	walk(v)

	return uses, nil
}

// isUnion reports whether s is a union with a type of its own: a oneOf or anyOf, not one collapsed to time.Time or to
// a pointer, nor made a [Tagged] struct.
func isUnion(s *openapi.Schema) bool {
	s = deref(s)

	return s != nil && s.Type == "" && (len(s.OneOf) > 0 || len(s.AnyOf) > 0) &&
		!collapsesOneOf(s) && nullableVariant(s) == nil && !isTaggedUnion(s)
}

// alternatives returns the alternatives of the union s.
func alternatives(s *openapi.Schema) openapi.SchemaList {
	s = deref(s)
	if len(s.OneOf) > 0 {
		return s.OneOf
	}

	return s.AnyOf
}

// fieldVariants returns one pointer field per alternative of a union but null.
func fieldVariants(variants openapi.SchemaList) ([]UnionVariant, []*openapi.Schema, error) {
	used := make(map[string]bool, len(variants))
	out := make([]UnionVariant, 0, len(variants))
	schemas := make([]*openapi.Schema, 0, len(variants))

	for i, v := range variants {
		// every field nil already means null, so a null variant needs no field
		if isNull(v) {
			continue
		}

		tp, err := SchemaGoType(v)
		if err != nil {
			return nil, nil, fmt.Errorf("variant %d: %w", i, err)
		}

		base := unionVariantFieldName(tp, i)

		fieldName := base
		for n := 2; used[fieldName]; n++ {
			fieldName = fmt.Sprintf("%s%d", base, n)
		}

		used[fieldName] = true

		uv := UnionVariant{FieldName: fieldName, Type: tp.String(), Zero: unsetValue(v, tp)}
		uv.Members, uv.Required, uv.Object = objectShape(v)
		uv.Pinned, uv.Enums = allowedValues(v)
		out = append(out, uv)
		schemas = append(schemas, v)
	}

	return out, schemas, nil
}

// unionVariants builds the fields of the union u, one per alternative but null, and the choices decoding picks from:
// the fields themselves, or, for an alternative that is a union of its own, each of its alternatives, setting the
// field to that union with the one alternative set. member is what tells the choices apart, if anything does.
func unionVariants(u *openapi.Schema, variants openapi.SchemaList) (fields, choices []UnionVariant, member string, err error) {
	fields, schemas, err := fieldVariants(variants)
	if err != nil {
		return nil, nil, "", err
	}

	nested := false
	leaves := make([]*openapi.Schema, 0, len(schemas))

	// expand adds the choices of an alternative: itself, or the choices of its own alternatives if it is a union
	var expand func(field UnionVariant, v *openapi.Schema, path []UnionStep, depth int) error

	expand = func(field UnionVariant, v *openapi.Schema, path []UnionStep, depth int) error {
		if !isUnion(v) || v.Ref == nil || depth > 8 {
			field.Path = path
			choices = append(choices, field)
			leaves = append(leaves, v)

			return nil
		}

		inner, innerSchemas, err := fieldVariants(alternatives(v))
		if err != nil {
			return err
		}

		nested = true

		path = append(slices.Clone(path), UnionStep{Field: field.FieldName, Type: field.Type})

		for j := range inner {
			if err := expand(inner[j], innerSchemas[j], path, depth+1); err != nil {
				return err
			}
		}

		return nil
	}

	for i, v := range schemas {
		if err := expand(fields[i], v, nil, 0); err != nil {
			return nil, nil, "", fmt.Errorf("variant %d: %w", i, err)
		}
	}

	// a discriminator's mapping names the alternatives, not their leaves
	decider := u
	if nested {
		decider = &openapi.Schema{}
	}

	// a union that can be null cannot have a discriminator come first
	member, values, ok := discriminate(decider, leaves)
	if !ok || countNull(variants) > 0 {
		return fields, choices, "", nil
	}

	for i := range choices {
		choices[i].Value = values[i]
	}

	if !nested {
		for i := range fields {
			fields[i].Value = values[i]
		}
	}

	return fields, choices, member, nil
}

func countNull(l openapi.SchemaList) int {
	n := 0
	for _, s := range l {
		if isNull(s) {
			n++
		}
	}

	return n
}

// fromAllOfSchema builds the struct of an allOf. A part that only this schema uses is folded into its fields, any
// other is embedded, and a union among the parts is held as a field of its own, decoded by the struct's own methods.
func fromAllOfSchema(name string, s *openapi.Schema, uses map[string]int, folded map[string]bool) (*Schema, error) {
	requiredSet := make(map[string]bool)
	for _, r := range s.Required {
		requiredSet[r] = true
	}

	out := &Schema{
		Name:        name,
		Description: getDescription(s, name),
		Kind:        SchemaKindAllOf,
	}

	addProperties := func(part *openapi.Schema) error {
		for _, r := range part.Required {
			requiredSet[r] = true
		}

		for jsonName, propRef := range part.Properties.ByIndex() {
			field, err := getField(jsonName, propRef, requiredSet)
			if err != nil {
				return fmt.Errorf("allOf property %q: %w", jsonName, err)
			}

			out.Fields = append(out.Fields, field)
			out.Members = append(out.Members, jsonName)
		}

		return nil
	}

	var unions, tagged []*openapi.Schema

	for _, entry := range s.AllOf {
		part := deref(entry)

		switch {
		case part == nil: // not resolved, so nothing is known of it but its name
			typeName, err := SchemaGoType(entry)
			if err != nil {
				return nil, err
			}

			out.Fields = append(out.Fields, Field{Type: typeName.String(), Embedded: true})
		case isTaggedUnion(part):
			tagged = append(tagged, entry)
		case len(part.OneOf) > 0 || len(part.AnyOf) > 0:
			if entry.Ref == nil {
				out.Unimplemented = "an allOf with an inline union"
				continue
			}

			unions = append(unions, entry)
		case entry.Ref == nil:
			if err := addProperties(entry); err != nil {
				return nil, err
			}
		case uses[entry.Ref.Identifier] == 1 && isFoldable(part):
			folded[entry.Ref.Identifier] = true

			if err := addProperties(part); err != nil {
				return nil, err
			}
		case hasJSONMethods(part):
			out.Unimplemented = "an allOf embedding a part that encodes itself"
		default:
			typeName, err := SchemaGoType(entry)
			if err != nil {
				return nil, err
			}

			out.Fields = append(out.Fields, Field{Type: typeName.String(), Embedded: true})

			members, _, _ := objectShape(part)
			out.Members = append(out.Members, members...)
		}
	}

	// the schema's own properties beside its allOf are members as much as its parts'
	if err := addProperties(s); err != nil {
		return nil, err
	}

	switch {
	case len(unions)+len(tagged) > 1:
		out.Unimplemented = "an allOf of more than one union"
		return out, nil
	case len(tagged) == 1:
		return foldTagged(out, tagged[0], uses, folded)
	case len(unions) == 0:
		return out, nil
	}

	entry := unions[0]
	u := deref(entry)

	isOneOf := len(u.OneOf) > 0
	alts := u.AnyOf
	if isOneOf {
		alts = u.OneOf
	}

	tp, err := SchemaGoType(entry)
	if err != nil {
		return nil, err
	}

	variants, choices, member, err := unionVariants(u, alts)
	if err != nil {
		return nil, err
	}

	if slices.ContainsFunc(alts, isNull) {
		out.Unimplemented = "an allOf whose union can be null"
	}

	// an alternative that is a union of its own counts by its alternatives
	if slices.ContainsFunc(choices, func(c UnionVariant) bool { return !c.Object }) {
		out.Unimplemented = "an allOf whose union has an alternative that is not a plain object"
	}

	slices.Sort(out.Members)
	out.Members = slices.Compact(out.Members)

	// named after its type, but exported even where the type is not, so a caller can reach it
	field := strings.ToUpper(tp.Name[:1]) + tp.Name[1:]

	out.Fields = append(out.Fields, Field{Name: field, Type: tp.Name, JSONTag: `json:"-"`, Required: true})
	out.AllOfUnion = &AllOfUnion{
		FieldName:     field,
		IsOneOf:       isOneOf,
		Discriminator: member,
		Variants:      variants,
		Choices:       choices,
	}

	return out, nil
}

// foldTagged adds the fields of entry, a union that is a [Tagged] struct, to out, the struct of an allOf it is part of,
// so that out checks its members as the union's struct would.
func foldTagged(out *Schema, entry *openapi.Schema, uses map[string]int, folded map[string]bool) (*Schema, error) {
	u := deref(entry)
	alts := alternatives(u)

	shape, _ := taggedUnion(u, alts)

	fields, tagged, ok, err := taggedFields(shape, out.Fields)
	if err != nil {
		return nil, err
	}

	// a member an embedded part declares has no field of out's own to check
	declared := map[string]bool{}
	for _, f := range out.Fields {
		declared[f.JSONName] = true
	}

	for _, f := range fields {
		if slices.Contains(out.Members, f.JSONName) && !declared[f.JSONName] {
			ok = false
		}
	}

	if ok && !declared[tagged.Tag] && slices.Contains(out.Members, tagged.Tag) {
		ok = false
	}

	if !ok {
		out.Unimplemented = "an allOf whose tagged union's members clash with its other parts"
		return out, nil
	}

	for _, f := range fields {
		out.Members = append(out.Members, f.JSONName)
	}

	out.Fields = append(out.Fields, fields...)
	out.Tagged = tagged

	slices.Sort(out.Members)
	out.Members = slices.Compact(out.Members)

	if entry.Ref != nil && uses[entry.Ref.Identifier] == 1 {
		folded[entry.Ref.Identifier] = true
	}

	foldAlternatives(shape, uses, folded)

	return out, nil
}

// markStreaming decides which unions decode as they read, by a discriminator that comes first: those whose every
// alternative is a struct able to decode one member at a time. It marks those structs, and the parts they embed, as
// needing that method.
func markStreaming(schemas []Schema) {
	byName := make(map[string]*Schema, len(schemas))
	for i := range schemas {
		byName[schemas[i].Name] = &schemas[i]
	}

	var memberDecodable func(name string, seen map[string]bool) bool

	memberDecodable = func(name string, seen map[string]bool) bool {
		s := byName[name]
		if s == nil || seen[name] {
			return s != nil
		}

		seen[name] = true

		// a Tagged struct checks its members once all are decoded, so it decodes as a whole
		switch {
		case s.Tagged != nil:
			return false
		case s.Kind == SchemaKindStruct:
			return true
		case s.Kind == SchemaKindAllOf:
			if s.Unimplemented != "" {
				return false
			}

			for _, f := range s.Fields {
				if f.Embedded && !memberDecodable(f.Type, seen) {
					return false
				}
			}

			return true
		default:
			return false
		}
	}

	var mark func(name string)

	mark = func(name string) {
		s := byName[name]
		if s == nil || s.MemberDecoder {
			return
		}

		s.MemberDecoder = true

		for _, f := range s.Fields {
			if f.Embedded {
				mark(f.Type)
			}
		}
	}

	streamable := func(variants []UnionVariant) bool {
		return len(variants) > 0 && !slices.ContainsFunc(variants, func(v UnionVariant) bool {
			return !memberDecodable(v.Type, map[string]bool{})
		})
	}

	for i := range schemas {
		s := &schemas[i]

		switch {
		case s.Kind == SchemaKindUnion && s.Discriminator != "" && streamable(s.Choices):
			s.Streamed = true

			for _, v := range s.Choices {
				mark(v.Type)
			}
		case s.AllOfUnion != nil && s.Unimplemented == "" && s.AllOfUnion.Discriminator != "" &&
			streamable(s.AllOfUnion.Choices) && memberDecodable(s.Name, map[string]bool{}):
			s.Streamed = true

			mark(s.Name)

			for _, v := range s.AllOfUnion.Choices {
				mark(v.Type)
			}
		}
	}
}

// unsetValue is what the field of the union alternative v, of Go type tp, holds while it is not set, if that needs no
// pointer: nil for a slice or a map, and "" for a string, so an empty string reads as not set. It is empty if the field
// is a pointer instead.
func unsetValue(v *openapi.Schema, tp *GoType) string {
	switch {
	case tp.IsPointer || tp.IsArrayOfSize > 0:
		return ""
	case tp.IsSlice || tp.IsNilable || tp.Name == "any" || strings.HasPrefix(tp.Name, "map["):
		return "nil"
	}

	if s := deref(v); s != nil && s.Type == openapi.TypeString {
		if st := stringGoType(s.Format); st.Name == "string" || st.Name == "types.Email" {
			return `""`
		}
	}

	return ""
}

// allowedValues returns the members of the object s that allow one value, by a const or a one-value enum, and those
// that allow the several values of an enum.
func allowedValues(s *openapi.Schema) (pinned []PinnedMember, enums []EnumMember) {
	for _, p := range orderedProperties(s) {
		v := deref(p.schema)
		if v == nil {
			continue
		}

		if len(v.Const) > 0 || len(v.Enum) == 1 {
			value := v.Const
			if len(value) == 0 {
				value = v.Enum[0]
			}

			if c, ok := compactJSON(value); ok {
				pinned = append(pinned, PinnedMember{Name: p.name, Value: c})
			}

			continue
		}

		if len(v.Enum) == 0 {
			continue
		}

		values := make([]string, 0, len(v.Enum))
		for _, e := range v.Enum {
			if c, ok := compactJSON(e); ok {
				values = append(values, c)
			}
		}

		enums = append(enums, EnumMember{Name: p.name, Values: values})
	}

	return pinned, enums
}

// compactJSON is v written compactly, as a decoder compares it.
func compactJSON(v jsontext.Value) (string, bool) {
	c := slices.Clone(v)
	if err := c.Compact(); err != nil {
		return "", false
	}

	return string(c), true
}
