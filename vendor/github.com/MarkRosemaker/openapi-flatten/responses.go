package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func operationResponses(d *openapi.Document, rs openapi.OperationResponses, opID string) error {
	for code, r := range rs.ByIndex() {
		alwaysMove := !code.IsSuccess()

		if err := responseRef(d, r, nameResponse(opID, code), alwaysMove); err != nil {
			return &errpath.ErrKey{Key: string(code), Err: err}
		}
	}

	return nil
}

// responses flattens the component responses, which are where they belong already. One an operation uses for a
// failure has its schema named even when it is a scalar, as an operation's own failure has.
func responses(d *openapi.Document, rs openapi.ResponsesByName) error {
	failures := failureResponses(d)

	for name, r := range rs.ByIndex() {
		if err := response(d, r.Value, name, failures[newRef("responses", name).Identifier]); err != nil {
			return &errpath.ErrKey{Key: string(name), Err: err}
		}
	}

	return nil
}

// failureResponses is the set of references to component responses an operation uses for a status other than a success.
func failureResponses(d *openapi.Document) map[string]bool {
	refs := map[string]bool{}

	for _, p := range d.Paths {
		for _, o := range p.Operations {
			for code, rs := range o.Responses {
				if !code.IsSuccess() && rs.Ref != nil {
					refs[rs.Ref.Identifier] = true
				}
			}
		}
	}

	return refs
}
