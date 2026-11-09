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
