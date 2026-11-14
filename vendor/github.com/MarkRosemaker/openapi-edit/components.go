package edit

import "github.com/MarkRosemaker/openapi"

// SchemaRef is a reference to the component schema name, as a schema to put where the component is wanted.
func SchemaRef(doc *openapi.Document, name string) *openapi.Schema {
	return &openapi.Schema{Ref: &openapi.SchemaRef{Identifier: schemaRefPrefix + name, Value: doc.Components.Schemas[name]}}
}

// RenameSchemasFunc renames each component schema to what rename makes of its name, as [RenameSchemas] does, such as to
// drop a suffix every name carries. A name rename leaves as it is stays.
//
// It fails, changing nothing, for the reasons RenameSchemas does.
func RenameSchemasFunc(doc *openapi.Document, rename func(string) string) error {
	to := map[string]string{}

	for name := range doc.Components.Schemas.ByIndex() {
		if newName := rename(name); newName != name {
			to[name] = newName
		}
	}

	return RenameSchemas(doc, to)
}
