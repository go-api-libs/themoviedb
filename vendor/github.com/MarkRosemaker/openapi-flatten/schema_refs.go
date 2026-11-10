package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func inlineSchemas(d *openapi.Document, ss openapi.Schemas, prefix string) error {
	for name, s := range ss.ByIndex() {
		if err := inlineSchema(d, s,
			joinName(prefix, name), false); err != nil {
			return &errpath.ErrKey{Key: name, Err: err}
		}
	}

	return nil
}
