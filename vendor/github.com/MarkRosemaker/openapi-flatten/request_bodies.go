package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func requestBodies(d *openapi.Document, rs openapi.RequestBodies) error {
	for name, r := range rs.ByIndex() {
		// NOTE: We are *not* calling RequestBodyRef here,
		// because we are calling this function from Components,
		// where the request body should already be.
		if err := requestBody(d, r.Value, name); err != nil {
			return &errpath.ErrKey{Key: name, Err: err}
		}
	}

	return nil
}
