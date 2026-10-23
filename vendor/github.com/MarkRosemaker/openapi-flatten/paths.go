package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func paths(d *openapi.Document, ps openapi.Paths) error {
	for p, pi := range ps.ByIndex() {
		if err := pathItem(d, pi); err != nil {
			return &errpath.ErrKey{Key: string(p), Err: err}
		}
	}

	return nil
}
