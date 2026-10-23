package ir

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"go/token"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

// SchemaGoType maps an openapi.Schema to its Go type string.
// After flattening, complex schemas are moved to components and referenced by $ref;
// a reference maps to the name of the component it points to.
func SchemaGoType(s *openapi.Schema) (*GoType, error) {
	if s.Ref != nil {
		// a component that declares no type of its own is used as it would be inline
		if declaresNoType(s.Ref.Value) {
			return SchemaGoType(s.Ref.Value)
		}

		// A date-time-or-int oneOf collapses to time.Time even when reached
		// via $ref, so no synthetic component type name leaks into the
		// generated code. A oneOf/anyOf union otherwise resolves to its own
		// generated pointer-bag type (see fromUnionSchema), so the ordinary
		// $ref-name resolution below already does the right thing for it.
		if s.Ref.Value != nil && isDateTimeOrIntegerOneOf(s.Ref.Value) {
			return &GoType{Name: "time.Time"}, nil
		}

		if s.Ref.Value != nil && isStringOrNumberOneOf(s.Ref.Value) {
			return &GoType{Name: "float64"}, nil
		}

		// "X or null" gets no type of its own either: it is a pointer to X.
		if v := nullableVariant(s.Ref.Value); v != nil {
			return nullableGoType(v)
		}
		// "#/components/schemas/Name" → "Name"
		parts := strings.Split(s.Ref.Identifier, "/")
		name := componentGoName(parts[len(parts)-1], s.Ref.Value)

		// The named type this $ref points at is itself array- or map-kind
		// (e.g. "type TimeEntries []TimeEntry"), so it's already nilable on
		// its own -- see [GoType.IsNilable].
		// A tuple is a struct, not a slice.
		isNilable := s.Ref.Value != nil && (s.Ref.Value.Type == openapi.TypeArray && len(s.Ref.Value.PrefixItems) == 0 ||
			mapValues(s.Ref.Value) != nil)

		return &GoType{Name: name, IsNilable: isNilable}, nil
	}

	switch s.Type {
	case openapi.TypeNull:
		// only ever seen as null: nothing but null fits
		return &GoType{Name: "struct{}", IsPointer: true}, nil
	case openapi.TypeBoolean:
		return &GoType{Name: "bool"}, nil
	case openapi.TypeInteger:
		return integerGoType(s.Format), nil
	case openapi.TypeNumber:
		return numberGoType(s.Format), nil
	case openapi.TypeString:
		return stringGoType(s.Format), nil
	case openapi.TypeArray:
		return arrayGoType(s)
	case openapi.TypeObject:
		return objectGoType(s)
	case "":
		if isDateTimeOrIntegerOneOf(s) {
			return &GoType{Name: "time.Time"}, nil
		}

		if isStringOrNumberOneOf(s) {
			return &GoType{Name: "float64"}, nil
		}

		if v := nullableVariant(s); v != nil {
			return nullableGoType(v)
		}
		// A oneOf/anyOf union normally goes through fromSchema as a named
		// component and gets a real generated pointer-bag type (see
		// fromUnionSchema). This is only reached for a union with no name to
		// give it (e.g. inline within array items or additionalProperties),
		// where there's nothing to generate a struct for.
		if isAnyOfOnly(s) || isOneOfOnly(s) {
			return &GoType{Name: "any"}, nil
		}

		// the empty schema accepts any value
		if len(s.Properties) == 0 && len(s.AllOf) == 0 {
			return &GoType{Name: "any"}, nil
		}

		return nil, fmt.Errorf("unsupported schema type: %q", s.Type)
	default:
		return nil, fmt.Errorf("unsupported schema type: %q", s.Type)
	}
}

// isDateTimeOrIntegerOneOf reports whether s is a oneOf composition of exactly
// two schemas: one string with format date-time, and one integer. The order of
// the two entries does not matter. When true, callers should surface a
// time.Time typed field with a custom (un)marshaller in the generated code.
func isDateTimeOrIntegerOneOf(s *openapi.Schema) bool {
	if len(s.OneOf) != 2 {
		return false
	}

	var hasDateTime, hasInteger bool
	for _, entry := range s.OneOf {
		v := deref(entry)
		if v == nil {
			return false
		}

		switch v.Type {
		case openapi.TypeString:
			if v.Format == openapi.FormatDateTime {
				hasDateTime = true
			}
		case openapi.TypeInteger:
			hasInteger = true
		default:
		}
	}

	return hasDateTime && hasInteger
}

// isStringOrNumberOneOf reports whether s is a oneOf of exactly two schemas, a string of no format and a number or an
// integer: a number an API sends as a string, such as "9.5", and now and then as a number. It is a float64, read from
// either and written as a string.
func isStringOrNumberOneOf(s *openapi.Schema) bool {
	if len(s.OneOf) != 2 {
		return false
	}

	var hasString, hasNumber bool

	for _, entry := range s.OneOf {
		switch v := deref(entry); {
		case v == nil:
			return false
		case v.Type == openapi.TypeString && v.Format == "" && len(v.Enum) == 0 && len(v.Const) == 0:
			hasString = true
		case v.Type == openapi.TypeNumber, v.Type == openapi.TypeInteger:
			hasNumber = true
		}
	}

	return hasString && hasNumber
}

// collapsesOneOf reports whether s is a oneOf that is one Go type of its own, not a union: a date-time or an integer,
// or a string or a number.
func collapsesOneOf(s *openapi.Schema) bool {
	return isDateTimeOrIntegerOneOf(s) || isStringOrNumberOneOf(s)
}

// isAnyOfOnly reports whether s is an untagged union expressed purely via
// anyOf (no type, allOf, or oneOf of its own).
func isAnyOfOnly(s *openapi.Schema) bool {
	return s.Type == "" && len(s.AnyOf) > 0 && len(s.AllOf) == 0 && len(s.OneOf) == 0
}

// isNull reports whether s, or the schema it refers to, is the null type.
func isNull(s *openapi.Schema) bool {
	v := deref(s)
	return v != nil && v.Type == openapi.TypeNull
}

// nullableVariant returns X if s is a oneOf or anyOf of just X and null, or nil otherwise.
func nullableVariant(s *openapi.Schema) *openapi.Schema {
	if s == nil || s.Type != "" || len(s.AllOf) > 0 || len(s.Properties) > 0 {
		return nil
	}

	variants := s.OneOf
	if len(variants) == 0 {
		variants = s.AnyOf
	} else if len(s.AnyOf) > 0 {
		return nil
	}

	if len(variants) != 2 {
		return nil
	}

	switch {
	case isNull(variants[0]) && !isNull(variants[1]):
		return variants[1]
	case isNull(variants[1]) && !isNull(variants[0]):
		return variants[0]
	default:
		return nil
	}
}

// nullableGoType is the Go type of X or null: a pointer to X if its zero value is a value of its own, or X itself.
func nullableGoType(v *openapi.Schema) (*GoType, error) {
	tp, err := SchemaGoType(v)
	if err != nil {
		return nil, err
	}

	// null otherwise reads as the zero value, which is all it needs to be when that is no value of X; a type that
	// decodes itself, such as a union, would refuse null
	if zeroIsAValue(v, tp) || hasJSONMethods(v) {
		tp.IsPointer = true
	}

	return tp, nil
}

// isOneOfOnly reports whether s is an untagged union expressed purely via
// oneOf (no type, allOf, or anyOf of its own), and is not the
// date-time-or-integer pattern that collapses to time.Time.
func isOneOfOnly(s *openapi.Schema) bool {
	return s.Type == "" && len(s.OneOf) > 0 && len(s.AllOf) == 0 && len(s.AnyOf) == 0 &&
		!collapsesOneOf(s)
}

func integerGoType(f openapi.Format) *GoType {
	switch f {
	case openapi.FormatInt32:
		return &GoType{Name: "int32"}
	case openapi.FormatInt64:
		return &GoType{Name: "int64"}
	case openapi.FormatUint:
		return &GoType{Name: "uint"}
	case openapi.FormatUint32:
		return &GoType{Name: "uint32"}
	case openapi.FormatUint64:
		return &GoType{Name: "uint64"}
	case openapi.FormatDuration:
		return &GoType{Name: "time.Duration"}
	case openapi.FormatDateTime:
		return &GoType{Name: "time.Time"}
	case openapi.FormatDate:
		return &GoType{Name: "civil.Date"}
	default: // no format, or one the generator does not know, which says nothing more than the type
		return &GoType{Name: "int"}
	}
}

func numberGoType(f openapi.Format) *GoType {
	if f == openapi.FormatFloat {
		return &GoType{Name: "float32"}
	}

	return &GoType{Name: "float64"}
}

func stringGoType(f openapi.Format) *GoType {
	switch f {
	case openapi.FormatUUID:
		return &GoType{Name: "uuid.UUID"}
	case openapi.FormatURI, openapi.FormatURIRef:
		return &GoType{Name: "url.URL"}
	case openapi.FormatEmail:
		return &GoType{Name: "types.Email"}
	case openapi.FormatDateTime:
		return &GoType{Name: "time.Time"}
	case openapi.FormatDate, openapi.FormatDateBasic:
		return &GoType{Name: "civil.Date"}
	case openapi.FormatIPv4, openapi.FormatIPv6:
		return &GoType{Name: "net.IP"}
	default:
		return &GoType{Name: "string"}
	}
}

func arrayGoType(s *openapi.Schema) (*GoType, error) {
	if s.Items == nil {
		return &GoType{Name: "[]any"}, nil
	}

	tp, err := SchemaGoType(s.Items)
	if err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}

	if s.MinItems > 0 && s.MaxItems != nil && s.MinItems == *s.MaxItems {
		tp.IsArrayOfSize = int(s.MinItems)
	} else {
		tp.IsSlice = true
	}

	return tp, nil
}

func objectGoType(s *openapi.Schema) (*GoType, error) {
	if values := mapValues(s); values != nil {
		tp, err := SchemaGoType(values)
		if err != nil {
			return nil, fmt.Errorf("additionalProperties: %w", err)
		}

		return &GoType{Name: "map[string]" + tp.Name, IsNilable: true}, nil
	}

	// Named objects with properties are moved to components by the flatten pass.
	return &GoType{Name: "struct{}"}, nil
}

// mapValues is the schema of the values of an object that is a map, or nil if it is a struct.
// additionalProperties: true on an object without properties allows any values, like the empty schema.
func mapValues(s *openapi.Schema) *openapi.Schema {
	switch ap := s.AdditionalProperties; {
	case ap == nil:
		return nil
	case ap.Schema != nil:
		return ap.Schema
	case ap.Allowed && len(s.Properties) == 0:
		return &openapi.Schema{}
	default:
		return nil
	}
}

// schemaRefPrefix starts every reference to a component schema.
const schemaRefPrefix = "#/components/schemas/"

// FromComponentSchemas converts a set of named component schemas to IR schemas.
func FromComponentSchemas(schemas openapi.Schemas) ([]Schema, error) {
	return fromComponentSchemas(schemas, nil)
}

// fromComponentSchemas converts the component schemas; uses counts the references to each, so that an allOf part
// only one schema refers to can be folded into that schema and need no type of its own.
func fromComponentSchemas(schemas openapi.Schemas, uses map[string]int) ([]Schema, error) {
	folded := map[string]bool{}
	keys := make([]string, 0, len(schemas))
	result := make([]Schema, 0, len(schemas))

	for key, s := range schemas.ByIndex() {
		name := componentGoName(key, s)

		irSchema, err := fromSchema(name, s, uses, folded)
		if err != nil {
			return nil, fmt.Errorf("schema %q: %w", name, err)
		}

		if irSchema != nil {
			keys = append(keys, key)
			result = append(result, *irSchema)
		}
	}

	kept := result[:0]
	for i, s := range result {
		if !folded[schemaRefPrefix+keys[i]] {
			kept = append(kept, s)
		}
	}

	pointRecursiveFields(kept)
	markOneWayFields(kept)
	markStreaming(kept)
	typeTags(kept)

	return kept, nil
}

func fromSchema(name string, s *openapi.Schema, uses map[string]int, folded map[string]bool) (*Schema, error) {
	switch s.Type {
	case openapi.TypeObject:
		if values := mapValues(s); values != nil {
			mapValueType, err := SchemaGoType(values)
			if err != nil {
				return nil, err
			}

			// A map of strings gets a named type like any other map: a
			// property referencing this component resolves to the component's
			// name, so declining to declare it leaves that name undefined.
			return &Schema{
				Name:        name,
				Description: getDescription(s, name),
				Kind:        SchemaKindMap,
				MapKey:      "string",
				MapValue:    mapValueType.String(),
			}, nil
		}

		if len(s.AllOf) > 0 {
			return fromAllOfSchema(name, s, uses, folded)
		}

		return fromObjectSchema(name, s)
	case openapi.TypeString, openapi.TypeInteger, openapi.TypeNumber, openapi.TypeBoolean:
		if len(s.Enum) > 0 {
			return fromEnumSchema(name, s)
		}

		return fromScalarSchema(name, s)
	case openapi.TypeArray:
		if len(s.PrefixItems) > 0 {
			return fromTupleSchema(name, s)
		}

		return fromArraySchema(name, s)
	case "":
		if collapsesOneOf(s) {
			return nil, nil // handled specially: SchemaGoType resolves the $ref straight to time.Time or float64
		}

		if nullableVariant(s) != nil {
			return nil, nil // handled specially: SchemaGoType resolves the $ref straight to a pointer
		}

		if len(s.AllOf) > 0 {
			return fromAllOfSchema(name, s, uses, folded)
		}

		if t, err := fromTaggedUnion(name, s, uses, folded); t != nil || err != nil {
			return t, err
		}

		if len(s.OneOf) > 0 {
			return fromUnionSchema(name, s, true)
		}

		if len(s.AnyOf) > 0 {
			return fromUnionSchema(name, s, false)
		}

		return nil, nil
	default:
		return nil, nil // scalar types are used inline
	}
}

// fromUnionSchema builds a pointer-bag union type from an untagged oneOf or
// anyOf composition: one nilable field per variant, with no discriminator.
// The caller distinguishes which variant matched by checking which field is
// non-nil after unmarshaling.
func fromUnionSchema(name string, s *openapi.Schema, isOneOf bool) (*Schema, error) {
	variants := s.AnyOf
	if isOneOf {
		variants = s.OneOf
	}

	unionVariants, choices, discriminator, err := unionVariants(s, variants)
	if err != nil {
		return nil, err
	}

	return &Schema{
		Name:          name,
		Description:   getDescription(s, name),
		Kind:          SchemaKindUnion,
		UnionVariants: unionVariants,
		Choices:       choices,
		IsOneOf:       isOneOf,
		Discriminator: discriminator,
	}, nil
}

// unionVariantFieldName derives an exported Go field name from a union
// variant's resolved type, e.g. "Card" for *Card, "UUID" for uuid.UUID.
func unionVariantFieldName(t *GoType, index int) string {
	name := t.Name
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:] // strip package qualifier, e.g. "uuid.UUID" -> "UUID"
	}

	switch {
	case name == "struct{}":
		name = "object" // an object with no properties of its own
	case strings.HasPrefix(name, "map[string]"):
		name = "map of " + strings.TrimPrefix(name, "map[string]")
	}

	// a type's own exported name is kept as it is, which Go-style casing could change, such as URL2 to Url2
	if !isExported(name) || !token.IsIdentifier(name) {
		name = strcase.ToGoPascal(name)
	}

	if name == "" {
		name = fmt.Sprintf("Variant%d", index+1)
	}

	return name
}

func getDescription(s *openapi.Schema, name string) string {
	if s.Description != "" {
		return s.Description
	}

	return fmt.Sprintf("%s defines a model", name)
}

// goNameOverride returns the schema's x-go-name extension, or "" if unset.
// See https://github.com/oapi-codegen/oapi-codegen/blob/main/docs/extensions.md#x-go-name.
func goNameOverride(s *openapi.Schema) string {
	if len(s.Extensions) == 0 {
		return ""
	}

	var ext struct {
		GoName string `json:"x-go-name"`
	}
	if err := json.Unmarshal(s.Extensions, &ext); err != nil {
		return ""
	}

	return ext.GoName
}

// enumNameOverrides returns the schema's x-enum-varnames or x-enumNames
// extension (the two are aliases; x-enum-varnames wins if somehow both are
// set), positionally matching s.Enum, or nil if neither is set.
// See https://github.com/oapi-codegen/oapi-codegen/blob/main/docs/extensions.md#x-enum-varnames--x-enumnames.
func enumNameOverrides(s *openapi.Schema) []string {
	if len(s.Extensions) == 0 {
		return nil
	}

	var ext struct {
		VarNames []string `json:"x-enum-varnames"`
		Names    []string `json:"x-enumNames"`
	}
	if err := json.Unmarshal(s.Extensions, &ext); err != nil {
		return nil
	}

	if len(ext.VarNames) > 0 {
		return ext.VarNames
	}

	return ext.Names
}

func getField(jsonName string, propRef *openapi.Schema, requiredSet map[string]bool) (Field, error) {
	goType, err := SchemaGoType(propRef)
	if err != nil {
		return Field{}, err
	}

	v := deref(propRef)

	required := requiredSet[jsonName] && !oneWay(propRef)
	if !required && zeroIsAValue(propRef, goType) {
		goType.IsPointer = true
	}

	fieldName := fieldGoName(jsonName)
	if override := goNameOverride(v); override != "" {
		fieldName = override
	}

	tag := buildJSONTag(jsonName, required)
	if isStringOrNumberOneOf(v) {
		tag = strings.TrimSuffix(tag, `"`) + `,string"` // written as the string it mostly is
	}

	return Field{
		Name:             fieldName,
		JSONName:         jsonName,
		Type:             goType.String(),
		JSONTag:          tag,
		Description:      cmp.Or(propRef.Description, v.Description),
		Required:         required,
		ReadOnly:         propRef.ReadOnly || v.ReadOnly,
		WriteOnly:        propRef.WriteOnly || v.WriteOnly,
		IsDateTimeOrInt:  isDateTimeOrIntegerOneOf(v),
		IsStringOrNumber: isStringOrNumberOneOf(v),
		IsUnix:           isUnix(goType.Name, v),
		IsDateBasic:      isDateBasic(goType.Name, v),
	}, nil
}

// isUnix reports whether a value of the Go type named, from schema s, is an integer of unix time: a date-time or a date
// that is an integer, or an array of them.
func isUnix(goType string, s *openapi.Schema) bool {
	for s != nil && s.Type == openapi.TypeArray { // the items of an array
		s = deref(s.Items)
	}

	return (goType == "time.Time" || goType == "civil.Date") && s != nil && s.Type == openapi.TypeInteger
}

// isDateBasic reports whether a value of the Go type named, from schema s, is a date in ISO 8601's basic form, such as
// "20260320", or an array of them.
func isDateBasic(goType string, s *openapi.Schema) bool {
	for s != nil && s.Type == openapi.TypeArray {
		s = deref(s.Items)
	}

	return goType == "civil.Date" && s != nil && s.Format == openapi.FormatDateBasic
}

func fromObjectSchema(name string, s *openapi.Schema) (*Schema, error) {
	requiredSet := make(map[string]bool, len(s.Required))
	for _, r := range s.Required {
		requiredSet[r] = true
	}

	fields := make([]Field, 0, len(s.Properties))
	for jsonName, propRef := range s.Properties.ByIndex() {
		field, err := getField(jsonName, propRef, requiredSet)
		if err != nil {
			return nil, fmt.Errorf("property %q: %w", jsonName, err)
		}

		fields = append(fields, field)
	}

	goNames := make([]*string, len(fields))
	for i := range fields {
		goNames[i] = &fields[i].Name
	}

	numberTaken(goNames)

	return &Schema{
		Name:        name,
		Description: getDescription(s, name),
		Kind:        SchemaKindStruct,
		Fields:      fields,
	}, nil
}

func fromEnumSchema(name string, s *openapi.Schema) (*Schema, error) {
	tp, err := enumBaseGoType(s.Type, s.Format)
	if err != nil {
		return nil, err
	}

	nameOverrides := enumNameOverrides(s)

	values := make([]EnumValue, len(s.Enum))
	for i, v := range s.Enum {
		display, literal, err := formatEnumValue(v, s.Type)
		if err != nil {
			return nil, fmt.Errorf("enum[%d]: %w", i, err)
		}

		goNameSource := display
		if i < len(nameOverrides) && nameOverrides[i] != "" {
			goNameSource = nameOverrides[i]
		}

		values[i] = EnumValue{
			GoName:  enumConstName(name, goNameSource),
			Value:   display,
			Literal: literal,
		}
	}

	goNames := make([]*string, len(values))
	for i := range values {
		goNames[i] = &values[i].GoName
	}

	numberTaken(goNames)

	return &Schema{
		Name:        name,
		Description: getDescription(s, name),
		Kind:        SchemaKindEnum,
		Type:        tp.String(),
		EnumValues:  values,
	}, nil
}

// enumBaseGoType returns the underlying Go type for an enum's declared schema type.
func enumBaseGoType(t openapi.DataType, f openapi.Format) (*GoType, error) {
	switch t {
	case openapi.TypeString:
		return stringGoType(f), nil
	case openapi.TypeInteger:
		return integerGoType(f), nil
	case openapi.TypeNumber:
		return numberGoType(f), nil
	case openapi.TypeBoolean:
		return &GoType{Name: "bool"}, nil
	default:
		return nil, fmt.Errorf("unsupported enum type: %q", t)
	}
}

// formatEnumValue converts a raw enum member (decoded from JSON as string,
// float64, or bool per the schema's declared type) into its human-readable
// display form and its Go source literal.
func formatEnumValue(v jsontext.Value, t openapi.DataType) (display, literal string, err error) {
	switch t {
	case openapi.TypeString:
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			return "", "", fmt.Errorf("unmarshalling %q into a string: %w", v, err)
		}

		return s, strconv.Quote(s), nil
	case openapi.TypeInteger:
		var i int64
		if err := json.Unmarshal(v, &i); err != nil {
			return "", "", fmt.Errorf("unmarshalling %q into a int64: %w", v, err)
		}

		s := strconv.FormatInt(i, 10)

		return s, s, nil
	case openapi.TypeNumber:
		var f float64
		if err := json.Unmarshal(v, &f); err != nil {
			return "", "", fmt.Errorf("unmarshalling %q into a float64: %w", v, err)
		}

		s := strconv.FormatFloat(f, 'g', -1, 64)

		return s, s, nil
	case openapi.TypeBoolean:
		var b bool
		if err := json.Unmarshal(v, &b); err != nil {
			return "", "", fmt.Errorf("unmarshalling %q into a bool: %w", v, err)
		}

		s := strconv.FormatBool(b)

		return s, s, nil
	default:
		return "", "", fmt.Errorf("unsupported enum type: %q", t)
	}
}

func fromArraySchema(name string, s *openapi.Schema) (*Schema, error) {
	aliasType, err := arrayGoType(s)
	if err != nil {
		return nil, err
	}

	return &Schema{
		Name:        name,
		Description: getDescription(s, name),
		Kind:        SchemaKindAlias,
		Type:        aliasType.String(),
		// a defined type would drop the methods uuid.UUID or time.Time encode themselves with
		IsTypeAlias: strings.Contains(aliasType.Name, ".") && !aliasType.IsPointer && !aliasType.IsSlice,
	}, nil
}

// fromTupleSchema declares a named component for a fixed-length,
// positionally-typed array (JSON Schema's prefixItems): a struct field per
// position, since Go has no tuple type and each position has its own type
// throughout every sample -- not one shared item type like a plain array.
func fromTupleSchema(name string, s *openapi.Schema) (*Schema, error) {
	width := len(strconv.Itoa(len(s.PrefixItems) - 1))

	fields := make([]Field, len(s.PrefixItems))
	for i, p := range s.PrefixItems {
		tp, err := SchemaGoType(p)
		if err != nil {
			return nil, &errpath.ErrField{Field: "prefixItems", Err: &errpath.ErrIndex{Index: i, Err: err}}
		}

		fieldName := fmt.Sprintf("Item%0*d", width, i)
		if override := goNameOverride(deref(p)); override != "" {
			fieldName = override
		}

		fields[i] = Field{
			Name:        fieldName,
			Type:        tp.String(),
			Description: cmp.Or(p.Description, deref(p).Description),
			IsUnix:      isUnix(tp.Name, deref(p)),
			IsDateBasic: isDateBasic(tp.Name, deref(p)),
		}
	}

	return &Schema{
		Name:        name,
		Description: getDescription(s, name),
		Kind:        SchemaKindTuple,
		Fields:      fields,
	}, nil
}

// fromScalarSchema declares a named component that is a plain scalar.
//
// The declaration is what makes the name usable: a $ref to this component
// resolves to its name, and a response body decoded into it can carry an Error
// method, which a bare string or int cannot.
func fromScalarSchema(name string, s *openapi.Schema) (*Schema, error) {
	aliasType, err := SchemaGoType(s)
	if err != nil {
		return nil, err
	}

	return &Schema{
		Name:        name,
		Description: getDescription(s, name),
		Kind:        SchemaKindAlias,
		Type:        aliasType.String(),
		// a defined type would drop the methods uuid.UUID or time.Time encode themselves with
		IsTypeAlias: strings.Contains(aliasType.Name, ".") && !aliasType.IsPointer && !aliasType.IsSlice,
	}, nil
}

// fieldGoName converts a JSON property name to an exported Go identifier.
func fieldGoName(jsonName string) string {
	// special case
	if strings.ToLower(jsonName) == "pdf" {
		return "PDF"
	}

	if suffix, ok := strings.CutPrefix(jsonName, "_"); ok {
		jsonName = fmt.Sprintf("Underscore %s", suffix)
	}

	// replace special characters before PascalCasing
	r := strings.NewReplacer(
		"+", " Plus ",
		".", " Dot ",
		"/", " ",
		"(", "",
		")", "",
		"C#", "CSharp",
		"F#", "CSharp",
	)

	sanitized := goWords(r.Replace(jsonName))
	sanitized = replaceLeadingDigits(sanitized)
	name := identOr(strcase.ToGoPascal(sanitized), jsonName)

	if first, _ := utf8.DecodeRuneInString(name); !unicode.IsUpper(first) {
		name = "X" + name // exported, or encoding leaves it out
	}

	// "Error" collides with the built-in error interface's Error() string
	// method (a struct can't have both a field and a method named Error),
	// so callers can never make the generated type satisfy error. Rename
	// the field; the json tag still uses the original JSON name.
	if name == "Error" {
		return "Err"
	}

	return name
}

var replInvalidChars = strings.NewReplacer(
	"#", " Sharp ",
	"/", " ",
	"+", " Plus ",
	".", " Dot ",
	"(", "",
	")", "",
	":", "",
	"'", "",
	"’", "",
)

// enumConstName builds the Go constant name for an enum value, e.g. MyEnum + "foo_bar" → MyEnumFooBar.
func enumConstName(typeName, value string) string {
	sanitized := goWords(replInvalidChars.Replace(value))
	sanitized = replaceLeadingDigits(sanitized)

	if sanitized != "" && len(sanitized) <= 3 && sanitized == strings.ToUpper(sanitized) && isIdent(sanitized) {
		return typeName + sanitized
	}

	return typeName + identOr(strcase.ToGoPascal(sanitized), value)
}

// goWords is s with any character a Go name cannot hold made a space, which only separates words, e.g. the comma in
// "Modify, only changes". "-" and "_" stay, as strcase separates words at them too.
func goWords(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			return r
		}

		return ' '
	}, s)
}

// isIdent reports whether s holds only what a Go name can.
func isIdent(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' })
}

// identOr is name, made from original, or where nothing of original made it into a name, a name of its own: Empty for
// an empty original, else its code points, e.g. U002A for "*".
func identOr(name, original string) string {
	if name = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return r
		}

		return -1
	}, name); name != "" {
		return name
	}

	if original == "" {
		return "Empty"
	}

	var b strings.Builder
	for _, r := range original {
		fmt.Fprintf(&b, "U%04X", r)
	}

	return b.String()
}

// numberTaken makes each of names, in turn, one no other before it has, by numbering a repeat: AB, then AB2.
func numberTaken(names []*string) {
	taken := make(map[string]bool, len(names))

	for _, name := range names {
		base := *name
		for i := 2; taken[*name]; i++ {
			*name = base + strconv.Itoa(i)
		}

		taken[*name] = true
	}
}

// replaceLeadingDigits converts every leading digit in the first word to its
// word equivalent, so the result can be used as a Go identifier.
// e.g. "4K" → "Four K", "1080p" → "One Zero Eight Zero p".
func replaceLeadingDigits(s string) string {
	if s == "" {
		return s
	}

	words := strings.Fields(s)
	if len(words) == 0 {
		return s
	}

	first := []rune(words[0])
	if len(first) == 0 || !unicode.IsDigit(first[0]) {
		return s
	}

	var parts []string

	i := 0
	for i < len(first) && unicode.IsDigit(first[i]) {
		parts = append(parts, digitWord(first[i]))
		i++
	}

	parts = append(parts, string(first[i:]))
	words[0] = strings.Join(parts, " ")

	return strings.Join(words, " ")
}

var digitWords = [10]string{
	"Zero", "One", "Two", "Three", "Four",
	"Five", "Six", "Seven", "Eight", "Nine",
}

func digitWord(r rune) string {
	d := int(r - '0')
	if d >= 0 && d < len(digitWords) {
		return digitWords[d]
	}

	return string(r)
}

// buildJSONTag computes the json struct tag for a field.
//
// An optional field is omitted when it holds its zero value, which is how a caller leaves it unset: nil for a pointer,
// a map or a slice, so an empty one is still sent. omitempty would not do: encoding/json/v2 omits with it only what
// encodes as an empty JSON value, so an unset time, number or boolean would still be sent, as "0001-01-01T00:00:00Z",
// 0 or false.
//
// A required field is always sent, zero value included: a required "", false or 0 is a real value.
func buildJSONTag(jsonName string, required bool) string {
	if required {
		return fmt.Sprintf(`json:"%s"`, jsonName)
	}

	return fmt.Sprintf(`json:"%s,omitzero"`, jsonName)
}

// oneWay reports whether only one direction carries the property p: responses alone if it is readOnly, requests alone
// if it is writeOnly. Neither requires it then, since the other leaves it out.
func oneWay(p *openapi.Schema) bool {
	v := deref(p)
	return p.ReadOnly || p.WriteOnly || v != nil && (v.ReadOnly || v.WriteOnly)
}

// requiredOf is what s requires of every value: its required properties but those only one direction carries.
func requiredOf(s *openapi.Schema) []string {
	return slices.DeleteFunc(slices.Clone(s.Required), func(name string) bool {
		p, ok := s.Properties[name]
		return ok && oneWay(p)
	})
}

// deref is the schema s stands for: the one it refers to, if it is a reference.
func deref(s *openapi.Schema) *openapi.Schema {
	if s != nil && s.Ref != nil {
		return s.Ref.Value
	}

	return s
}

// declaresNoType reports whether fromSchema declares no type for the component s: it is null, only a reference, or the empty schema.
func declaresNoType(s *openapi.Schema) bool {
	return s != nil && (s.Type == openapi.TypeNull ||
		s.Type == "" && len(s.AllOf) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0 && len(s.Properties) == 0)
}

// reNotInGoName matches what a component name may hold but a Go identifier may not, e.g. the "-" in "Keypoint-Input".
var reNotInGoName = regexp.MustCompile(`[^A-Za-z0-9_]+(.?)`)

// componentGoName is the Go type name of the component schema s, named name: its x-go-name, or name made a valid identifier.
func componentGoName(name string, s *openapi.Schema) string {
	if s != nil {
		if override := goNameOverride(s); override != "" {
			return override
		}
	}

	return reNotInGoName.ReplaceAllStringFunc(name, func(m string) string {
		return strings.ToUpper(reNotInGoName.ReplaceAllString(m, "$1"))
	})
}

// markOneWayFields lists each struct's readOnly and writeOnly fields, its own and those of the parts it embeds, which
// encoding inlines, so that the struct's encoding leaves them out.
func markOneWayFields(schemas []Schema) {
	byName := make(map[string]*Schema, len(schemas))
	for i := range schemas {
		byName[schemas[i].Name] = &schemas[i]
	}

	var paths func(s *Schema, of func(Field) bool, seen map[string]bool) []string

	paths = func(s *Schema, of func(Field) bool, seen map[string]bool) []string {
		var out []string

		for _, f := range s.Fields {
			switch {
			case !f.Embedded:
				if of(f) {
					out = append(out, f.Name)
				}
			case !strings.HasPrefix(f.Type, "*") && byName[f.Type] != nil && !seen[f.Type]:
				// a pointer to a part is shared with the caller, so it is not changed
				seen[f.Type] = true
				for _, p := range paths(byName[f.Type], of, seen) {
					out = append(out, f.Type+"."+p)
				}
			}
		}

		return out
	}

	for i := range schemas {
		s := &schemas[i]
		s.ReadOnly = paths(s, func(f Field) bool { return f.ReadOnly }, map[string]bool{s.Name: true})
		s.WriteOnly = paths(s, func(f Field) bool { return f.WriteOnly }, map[string]bool{s.Name: true})
	}
}
