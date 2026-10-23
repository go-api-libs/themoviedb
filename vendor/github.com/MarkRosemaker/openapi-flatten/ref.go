package flatten

import (
	"fmt"

	"github.com/MarkRosemaker/openapi"
)

func newRef(tp, name string) *openapi.Reference {
	return &openapi.Reference{Identifier: fmt.Sprintf("#/components/%s/%s", tp, name)}
}
