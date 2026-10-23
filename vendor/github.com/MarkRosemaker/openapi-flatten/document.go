package flatten

import (
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// Config configures [Document].
type Config struct {
	// MarkOrigin writes [ExtensionOrigin] on each component schema Document moves out of the document, so that a later
	// step can tell a name flatten made up from one the specification gave. openapi-compress reads and removes it.
	MarkOrigin bool
}

// Document flattens an entire OpenAPI document so it contains no nested objects.
func Document(d *openapi.Document, cfg Config) error {
	existing := make(map[string]bool, len(d.Components.Schemas))
	for name := range d.Components.Schemas {
		existing[name] = true
	}

	moveCommonPathPrefix(d)

	if err := paths(d, d.Paths); err != nil {
		return &errpath.ErrField{Field: "paths", Err: err}
	}

	if err := components(d, d.Components); err != nil {
		return &errpath.ErrField{Field: "components", Err: err}
	}

	hoistParams(d)

	if cfg.MarkOrigin {
		created := map[string]bool{}
		for name := range d.Components.Schemas {
			created[name] = !existing[name]
		}

		markOrigins(d, created)
	}

	return nil
}
