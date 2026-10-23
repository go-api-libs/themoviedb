package merge

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// Response merges b into a: their descriptions and content.
func Response(a, b *openapi.Response) error {
	a.Description = mergeString(a.Description, b.Description)

	if err := Content(&a.Content, b.Content); err != nil {
		return &errpath.ErrField{Field: "content", Err: err}
	}

	return extensions(a.Extensions, b.Extensions)
}
