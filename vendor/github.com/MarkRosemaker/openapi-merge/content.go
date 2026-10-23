package merge

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// Content merges b into a: a media type only in b is added, one in both is merged.
func Content(a *openapi.Content, b openapi.Content) error {
	for mr, mtB := range b.ByIndex() {
		mtA, ok := (*a)[mr]
		if !ok {
			a.Set(mr, mtB)
			continue
		}

		if err := MediaType(mtA, mtB); err != nil {
			return &errpath.ErrKey{Key: string(mr), Err: err}
		}
	}

	return nil
}
