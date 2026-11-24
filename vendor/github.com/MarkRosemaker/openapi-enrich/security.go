package enrich

import (
	"net/http"
	"slices"

	"github.com/MarkRosemaker/openapi"
)

// anonymous is the security requirement met without credentials: listed beside others, it makes them optional.
var anonymous = openapi.SecurityRequirement{}

// effectiveSecurity is the security an operation is held to: its own, or else the document's. It is nil while
// nothing is known, as for an operation only just created.
func effectiveSecurity(doc *openapi.Document, op *openapi.Operation, created bool) openapi.SecurityRequirements {
	if op.Security != nil || created {
		return op.Security
	}

	return doc.Security
}

// requireAuth records that op was called with the credential req asks for.
func requireAuth(doc *openapi.Document, op *openapi.Operation, created bool, req openapi.SecurityRequirement) {
	eff := effectiveSecurity(doc, op, created)

	switch {
	case eff.Contains(req):
	case eff == nil:
		op.Security = openapi.SecurityRequirements{req}
	case len(eff) == 0:
		// it was called without credentials before, so they are optional
		op.Security = openapi.SecurityRequirements{anonymous, req}
	default:
		op.Security = append(slices.Clone(eff), req)
	}
}

// allowAnonymous records that op was called without credentials.
func allowAnonymous(doc *openapi.Document, op *openapi.Operation, created bool) {
	eff := effectiveSecurity(doc, op, created)

	switch {
	case eff == nil:
		op.Security = openapi.SecurityRequirements{}
	case len(eff) == 0, eff.Contains(anonymous):
	default:
		op.Security = append(openapi.SecurityRequirements{anonymous}, eff...)
	}
}

// hoistSecurity states the security once at the document level, removing it from each operation, if every operation
// is held to the same.
//
// Only a whole list is hoisted: an operation's list replaces the document's, so moving part of it would change what
// the operation requires. If no operation needs credentials, the document says so only where it defines a scheme.
func hoistSecurity(doc *openapi.Document) {
	var ops []*openapi.Operation
	for _, pi := range doc.Paths {
		for _, op := range pi.Operations {
			ops = append(ops, op)
		}
	}

	if len(ops) == 0 {
		return
	}

	common := effectiveSecurity(doc, ops[0], false)
	for _, op := range ops[1:] {
		if !sameRequirements(effectiveSecurity(doc, op, false), common) {
			return
		}
	}

	if len(common) == 0 {
		common = nil
		if len(doc.Components.SecuritySchemes) > 0 {
			common = openapi.SecurityRequirements{}
		}
	}

	doc.Security = common
	for _, op := range ops {
		op.Security = nil
	}
}

// sameRequirements reports whether a and b list the same requirements, in any order.
func sameRequirements(a, b openapi.SecurityRequirements) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(r openapi.SecurityRequirement) bool {
		return !b.Contains(r)
	})
}

// apiKeyScheme is the name of the document's apiKey scheme sent in as name, if it has one: the parameter it names
// carries a credential, not a value of the operation.
func apiKeyScheme(doc *openapi.Document, in openapi.SecuritySchemeIn, name string) (openapi.SecuritySchemeName, bool) {
	for n, s := range doc.Components.SecuritySchemes.ByIndex() {
		if s == nil || s.Value == nil || s.Value.Type != openapi.SecuritySchemeTypeAPIKey || s.Value.In != in {
			continue
		}

		// a header's name is case-insensitive, a query parameter's or cookie's is not
		if s.Value.Name == name || in == openapi.SecuritySchemeInHeader && http.CanonicalHeaderKey(s.Value.Name) == name {
			return n, true
		}
	}

	return "", false
}
