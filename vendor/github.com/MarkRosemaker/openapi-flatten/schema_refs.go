package flatten

import (
	"fmt"
	"strings"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

func inlineSchemas(d *openapi.Document, ss openapi.Schemas, prefix string) error {
	for name, s := range ss.ByIndex() {
		if err := inlineSchema(d, s,
			strcase.ToGoPascal(fmt.Sprintf("%s %s", prefix, strings.ReplaceAll(name, "/", " "))), false); err != nil {
			return &errpath.ErrKey{Key: name, Err: err}
		}
	}

	return nil
}
