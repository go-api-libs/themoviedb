package merge

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// Schemas merges b into a: a schema only in b is added, one in both is merged.
func Schemas(a *openapi.Schemas, b openapi.Schemas) error {
	return schemas(*a, a, b)
}

func schemas(aAll openapi.Schemas, aToSet *openapi.Schemas, b openapi.Schemas) error {
	for keyB, sB := range b.ByIndex() {
		sA, ok := aAll[keyB]
		if !ok {
			aToSet.Set(keyB, sB) // add the property
			continue
		}

		// merge the properties
		if err := Schema(deref(sA), deref(sB), false); err != nil {
			return &errpath.ErrKey{Key: keyB, Err: err}
		}
	}

	return nil
}
