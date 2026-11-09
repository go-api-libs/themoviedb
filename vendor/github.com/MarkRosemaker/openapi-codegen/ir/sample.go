package ir

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
)

// ParamSample is a value a generated test sets a parameter to, and what the request must then carry for it.
type ParamSample struct {
	Param
	// Literal is the Go expression the test assigns to the parameter's field.
	Literal string
	// Wire is the value the request must carry under the parameter's name.
	Wire string
}

// ParamSamples are the query and header parameters of op a generated test can set and then find in the request, each
// with a value to set it to. A query parameter may be a scalar or an array of scalars, which the test sets to one
// item, the same on the wire in every style; a header parameter must be a scalar. Objects, maps and values with no
// plain text form, such as times, are left out.
func (doc Document) ParamSamples(op Operation) []ParamSample {
	var samples []ParamSample

	for _, p := range op.QueryParams {
		p.In = "query"

		switch {
		case p.Props != nil, p.MapValue != nil:
		case p.Item != nil:
			if lit, wire, ok := doc.scalarSample(*p.Item, len(samples)); ok && strings.HasPrefix(p.Type, "[]") {
				samples = append(samples, ParamSample{Param: p, Literal: p.Type + "{" + lit + "}", Wire: wire})
			}
		default:
			if lit, wire, ok := doc.scalarSample(p, len(samples)); ok {
				samples = append(samples, ParamSample{Param: p, Literal: lit, Wire: wire})
			}
		}
	}

	for _, p := range op.HeaderParams {
		p.In = "header"

		if lit, wire, ok := doc.scalarSample(p, len(samples)); ok {
			samples = append(samples, ParamSample{Param: p, Literal: lit, Wire: wire})
		}
	}

	return samples
}

// scalarSample is a value for p other than its zero value, which the client would leave out, as a Go expression of
// p's type and as the text the client must send for it. A string is p's own name and a number counts up with n, the
// parameter's position, so a client that sends one parameter's value under another's name is found out.
func (doc Document) scalarSample(p Param, n int) (literal, wire string, ok bool) {
	if p.IsEnum {
		i := slices.IndexFunc(doc.Schemas, func(s Schema) bool { return s.Name == p.Type })
		if i < 0 || len(doc.Schemas[i].EnumValues) == 0 {
			return "", "", false
		}

		// The last member, since a first one is the likeliest to be what a server assumes when it is left out.
		v := doc.Schemas[i].EnumValues[len(doc.Schemas[i].EnumValues)-1]

		return v.GoName, v.Value, v.GoName != "" && v.Value != ""
	}

	switch cmp.Or(p.BaseType, p.Type) {
	case "string":
		literal, wire = strconv.Quote(p.JSONName), p.JSONName
	case "bool":
		literal, wire = "true", "true"
	case "int", "int32", "int64", "uint", "uint32", "uint64":
		literal = strconv.Itoa(n + 2)
		wire = literal
	case "float32", "float64":
		literal = strconv.Itoa(n+2) + ".5"
		wire = literal
	default:
		return "", "", false
	}

	if p.BaseType != "" {
		literal = p.Type + "(" + literal + ")"
	}

	return literal, wire, true
}

// HasParamSamples reports whether a generated test can set a parameter of any operation, see [Document.ParamSamples].
func (doc Document) HasParamSamples() bool {
	return slices.ContainsFunc(doc.Operations, func(op Operation) bool { return len(doc.ParamSamples(op)) > 0 })
}

// ErrorResponses are the responses of op other than a success that the client decodes into a type of their own and
// returns as a custom error, each under a status code of its own rather than a range or the default.
func (op Operation) ErrorResponses() Responses {
	var rs Responses

	for _, r := range op.Responses {
		if r.IsSuccess || !r.IsJSON() || r.GoType == nil || r.GoType.String() != r.GoType.Name {
			continue
		}

		if _, err := strconv.Atoi(r.StatusCode); err != nil {
			continue
		}

		rs = append(rs, r)
	}

	return rs
}

// HasErrorResponses reports whether any operation has a response [Operation.ErrorResponses] lists.
func (doc Document) HasErrorResponses() bool {
	return slices.ContainsFunc(doc.Operations, func(op Operation) bool { return len(op.ErrorResponses()) > 0 })
}

// StrictSuccess is the success response of op the client decodes into a struct of the package's own, which rejects a
// member it does not declare where a caller's type passed to the WithResult variant accepts it, or nil. In debug mode
// every type is decoded leniently, so there is none.
func (doc Document) StrictSuccess(op Operation) *Response {
	if doc.Debug {
		return nil
	}

	return doc.structSuccess(op)
}

// LenientSuccess is the success response of op whose struct debug mode decodes leniently, adding the call to the
// debug file, where the client would otherwise reject a member the struct does not declare, or nil outside debug mode.
func (doc Document) LenientSuccess(op Operation) *Response {
	if !doc.Debug {
		return nil
	}

	return doc.structSuccess(op)
}

// structSuccess is the success response of op the client decodes into a struct of the package's own, or nil.
func (doc Document) structSuccess(op Operation) *Response {
	if op.EmptySuccess || op.SuccessReturn == nil {
		return nil
	}

	i := slices.IndexFunc(op.Responses, func(r Response) bool { return r.IsSuccess })
	if i < 0 {
		return nil
	}

	r := op.Responses[i]
	if !r.IsJSON() || r.GoType == nil || r.GoType.String() != r.GoType.Name {
		return nil
	}

	if !slices.ContainsFunc(doc.Schemas, func(s Schema) bool { return s.Name == r.GoType.Name && s.Kind == SchemaKindStruct }) {
		return nil
	}

	// A union's minimal literal sets its first variant but not what tells the variants apart, so it may not decode.
	if doc.requiresUnion(r.GoType.Name, map[string]bool{}) {
		return nil
	}

	return &r
}

// requiresUnion reports whether a value of goType holds a union, tagged or not, itself or in a required field, which
// [Document.MinimalLiteral] sets unless it is a pointer, whose type no schema is named.
func (doc Document) requiresUnion(goType string, seen map[string]bool) bool {
	if seen[goType] {
		return false
	}

	seen[goType] = true

	i := slices.IndexFunc(doc.Schemas, func(s Schema) bool { return s.Name == goType })
	if i < 0 {
		return false
	}

	s := doc.Schemas[i]
	if s.Kind == SchemaKindUnion || s.Tagged != nil {
		return true
	}

	return slices.ContainsFunc(s.Fields, func(f Field) bool {
		return (f.Required || f.Embedded) && doc.requiresUnion(f.Type, seen)
	})
}

// HasStructSuccess reports whether any operation has a response [Document.StrictSuccess] or
// [Document.LenientSuccess] finds.
func (doc Document) HasStructSuccess() bool {
	return slices.ContainsFunc(doc.Operations, func(op Operation) bool { return doc.structSuccess(op) != nil })
}

// ValueLiteral is a Go expression of goType that marshals: [Document.MinimalLiteral] for a struct, union or allOf,
// the zero value of anything else, such as a named string, which a composite literal cannot build.
func (doc Document) ValueLiteral(goType string) string {
	if slices.ContainsFunc(doc.Schemas, func(s Schema) bool {
		return s.Name == goType && (s.Kind == SchemaKindStruct || s.Kind == SchemaKindUnion || s.Kind == SchemaKindAllOf)
	}) {
		return doc.MinimalLiteral(goType)
	}

	return "*new(" + goType + ")"
}
