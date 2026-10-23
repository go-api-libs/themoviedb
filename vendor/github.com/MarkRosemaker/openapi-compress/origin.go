package compress

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/MarkRosemaker/openapi"
)

// extensionOrigin is the extension openapi-flatten's MarkOrigin writes on each component schema it named itself.
const extensionOrigin = "x-flattened-from"

// takeOrigins removes extensionOrigin from the component schemas of d, and returns the names of those that had it.
func takeOrigins(d *openapi.Document) map[string]bool {
	derived := map[string]bool{}

	for name, s := range d.Components.Schemas {
		if len(s.Extensions) == 0 {
			continue
		}

		var ext map[string]jsontext.Value
		if err := json.Unmarshal(s.Extensions, &ext); err != nil {
			continue
		}

		if _, ok := ext[extensionOrigin]; !ok {
			continue
		}

		derived[name] = true

		delete(ext, extensionOrigin)

		if len(ext) == 0 {
			s.Extensions = nil
		} else if b, err := json.Marshal(ext, json.Deterministic(true)); err == nil {
			s.Extensions = b
		}
	}

	return derived
}

// compareBool orders false before true.
func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	default:
		return -1
	}
}
