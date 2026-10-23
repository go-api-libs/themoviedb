package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func schemas(d *openapi.Document, ss openapi.Schemas) error {
	for name, s := range ss.ByIndex() {
		if err := schema(d, s, name); err != nil {
			return &errpath.ErrKey{Key: name, Err: err}
		}
	}

	return nil
}
