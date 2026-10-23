package merge

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// MediaType merges b into a: their schemas, and b's example where a has none.
func MediaType(a, b *openapi.MediaType) error {
	if b.Schema != nil {
		if a.Schema != nil {
			if err := Schema(deref(a.Schema), deref(b.Schema), false); err != nil {
				return &errpath.ErrField{Field: "schema", Err: err}
			}
		} else {
			a.Schema = b.Schema
		}
	}

	if a.Example == nil {
		a.Example = b.Example
	}

	return extensions(a.Extensions, b.Extensions)
}
