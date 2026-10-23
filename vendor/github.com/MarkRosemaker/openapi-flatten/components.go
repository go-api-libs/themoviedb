package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func components(d *openapi.Document, c openapi.Components) error {
	if err := schemas(d, c.Schemas); err != nil {
		return &errpath.ErrField{Field: "schemas", Err: err}
	}

	if err := responses(d, c.Responses); err != nil {
		return &errpath.ErrField{Field: "responses", Err: err}
	}

	if err := parameters(d, c.Parameters); err != nil {
		return &errpath.ErrField{Field: "parameters", Err: err}
	}

	if err := requestBodies(d, c.RequestBodies); err != nil {
		return &errpath.ErrField{Field: "requestBodies", Err: err}
	}

	return nil
}
