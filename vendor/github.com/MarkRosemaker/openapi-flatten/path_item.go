package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func pathItem(d *openapi.Document, pi *openapi.PathItem) error {
	if err := parameterList(d, pi.Parameters); err != nil {
		return &errpath.ErrField{Field: "parameters", Err: err}
	}

	for method, op := range pi.Operations {
		if err := operation(d, op); err != nil {
			return &errpath.ErrField{Field: method, Err: err}
		}
	}

	return nil
}
