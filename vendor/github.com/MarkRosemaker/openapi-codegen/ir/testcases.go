package ir

import (
	"slices"
	"strconv"
)

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

// DebugDecoded is the success response of op the client decodes as JSON into a value it returns, which debug mode
// records when it fails to, or nil outside debug mode.
func (doc Document) DebugDecoded(op Operation) *Response {
	if !doc.Debug || op.EmptySuccess || op.SuccessReturn == nil {
		return nil
	}

	i := slices.IndexFunc(op.Responses, func(r Response) bool { return r.IsSuccess })
	if i < 0 || !op.Responses[i].IsJSON() {
		return nil
	}

	r := op.Responses[i]

	return &r
}

// LenientError reports whether debug mode decodes r, an error response, leniently from a minimal value of its type
// with an unknown member: in debug mode, for a struct that holds no union, see [Document.StrictSuccess].
func (doc Document) LenientError(r Response) bool {
	return doc.Debug && r.GoType != nil &&
		slices.ContainsFunc(doc.Schemas, func(s Schema) bool { return s.Name == r.GoType.Name && s.Kind == SchemaKindStruct }) &&
		!doc.requiresUnion(r.GoType.Name, map[string]bool{})
}

// BodilessFailures are the responses of op other than a success that declare no body, which the client reports by
// their status, each under a status code of its own.
func (op Operation) BodilessFailures() Responses {
	var rs Responses

	for _, r := range op.Responses {
		if r.IsSuccess || r.GoType != nil {
			continue
		}

		if _, err := strconv.Atoi(r.StatusCode); err == nil {
			rs = append(rs, r)
		}
	}

	return rs
}

// NeedsBodyServer reports whether a generated test answers with a body of its choosing, see newBodyServer.
func (doc Document) NeedsBodyServer() bool {
	return doc.HasErrorResponses() || doc.HasStructSuccess() || slices.ContainsFunc(doc.Operations, func(op Operation) bool {
		return len(op.BodilessFailures()) > 0 || doc.DebugDecoded(op) != nil
	})
}

// NeedsUnknownMember reports whether a generated test sends a value with a member no type declares, see
// withUnknownMember.
func (doc Document) NeedsUnknownMember() bool {
	return doc.HasStructSuccess() || slices.ContainsFunc(doc.Operations, func(op Operation) bool {
		return slices.ContainsFunc(op.ErrorResponses(), doc.LenientError)
	})
}
