package flatten

import (
	"strings"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

// nameResponse returns a human-readable name for the response.
func nameResponse(opID string, code openapi.StatusCode) string {
	statusText := code.StatusText()
	if statusText == "" {
		statusText = string(code)
	}

	return strcase.ToGoPascal(strings.Join([]string{opID, statusText}, " "))
}

func response(d *openapi.Document, r *openapi.Response, rspName string, alwaysMove bool) error {
	if err := content(d, r.Content, rspName, "Response", alwaysMove); err != nil {
		return &errpath.ErrField{Field: "content", Err: err}
	}

	return nil
}

func responseRef(d *openapi.Document, r *openapi.ResponseRef, rspName string, alwaysMove bool) error {
	if r.Ref != nil {
		return nil
	}

	// reference the response in the components
	rspName = uniqueName(d.Components.Responses, rspName)
	d.Components.Responses.Set(rspName, &openapi.ResponseRef{Value: r.Value})
	r.Ref = newRef("responses", rspName)

	return response(d, r.Value, rspName, alwaysMove) // flatten the response itself
}
