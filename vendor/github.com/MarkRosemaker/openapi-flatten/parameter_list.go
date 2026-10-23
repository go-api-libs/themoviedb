package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func parameterList(d *openapi.Document, p openapi.ParameterList) error {
	for i, param := range p {
		if err := parameterRef(d, param); err != nil {
			return &errpath.ErrIndex{Index: i, Err: err}
		}
	}

	return nil
}
