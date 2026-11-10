package flatten

import (
	"strings"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// nameMediaType names the schema of a media type after the response, request body or parameter it is in, without its
// kind, tp, such as "Response". Two media types of one are told apart by uniqueName.
func nameMediaType(name, tp string) string {
	return joinName(strings.TrimSuffix(name, tp), "")
}

// joinName is the name of what is in prefix, a component's name, after name, as a property's key. A prefix that is a
// Go name already stays as it is: flatten made it, or the specification wrote it so, and casing it again could only
// change it, as GetAVOk to GetAvOk.
func joinName(prefix, name string) string {
	if isGoName(prefix) {
		return prefix + goName(name)
	}

	return goName(prefix, name)
}

// isGoName reports whether name is an exported Go name of ASCII letters and digits.
func isGoName(name string) bool {
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return false
	}

	return !strings.ContainsFunc(name, func(r rune) bool {
		return !('a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9')
	})
}

func mediaType(d *openapi.Document, mt *openapi.MediaType, mtName string, alwaysMove bool) error {
	if mt.Schema != nil {
		if title := mt.Schema.Title; title != "" {
			mtName = goName(title)
		}

		if err := inlineSchema(d, mt.Schema, mtName, alwaysMove); err != nil {
			return &errpath.ErrField{Field: "schema", Err: err}
		}
	}

	return nil
}
