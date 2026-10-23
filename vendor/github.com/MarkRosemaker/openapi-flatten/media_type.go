package flatten

import (
	"strings"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

// nameMediaType names the schema of a media type after the response, request body or parameter it is in, without its
// kind, tp, such as "Response". Two media types of one are told apart by uniqueName.
func nameMediaType(name, tp string) string {
	return strcase.ToGoPascal(strings.TrimSuffix(name, tp))
}

func mediaType(d *openapi.Document, mt *openapi.MediaType, mtName string, alwaysMove bool) error {
	if mt.Schema != nil {
		if title := mt.Schema.Title; title != "" {
			mtName = strcase.ToGoPascal(title)
		}

		if err := inlineSchema(d, mt.Schema, mtName, alwaysMove); err != nil {
			return &errpath.ErrField{Field: "schema", Err: err}
		}
	}

	return nil
}
