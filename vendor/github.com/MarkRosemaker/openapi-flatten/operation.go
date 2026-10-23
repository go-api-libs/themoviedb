package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func operation(d *openapi.Document, o *openapi.Operation) error {
	if err := parameterList(d, o.Parameters); err != nil {
		return &errpath.ErrField{Field: "parameters", Err: err}
	}

	if o.RequestBody != nil {
		if err := requestBodyRef(d, o.RequestBody, nameRequestBody(o.OperationID)); err != nil {
			return &errpath.ErrField{Field: "requestBody", Err: err}
		}
	}

	if err := operationResponses(d, o.Responses, o.OperationID); err != nil {
		return &errpath.ErrField{Field: "responses", Err: err}
	}

	return nil
}
