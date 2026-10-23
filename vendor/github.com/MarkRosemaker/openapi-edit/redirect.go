package edit

import (
	"encoding/json/v2"
	"maps"
	"slices"

	"github.com/MarkRosemaker/openapi"
)

// RedirectSchema repoints every reference to oldName at newName and removes
// oldName from components.schemas. It does not read or change either
// schema's own definition — newName's shape is left exactly as it was, and
// oldName's is discarded along with oldName itself, not merged into
// newName's. Combining two schemas' definitions into one wider shape is a
// distinct, unrelated operation; see [openapi-merge] for that.
//
// It differs from [RenameSchema], which refuses to rename a schema onto a
// name that already exists ([ErrSchemaExists]): redirecting onto an existing
// schema is exactly the point here, typically because several near-duplicate
// schemas (e.g. ones an OpenAPI generator produced one per endpoint, that
// happen to describe the same thing) are being consolidated onto one of
// them.
//
// If description is non-empty, it becomes the $ref-level description on
// every reference this repoints, replacing whatever description that
// reference already had. The usual reason to set it is that oldName's own
// definition — its bounds, its wording — is about to be discarded once
// oldName is gone; setting description is how that information survives on
// the references that used it, rather than being lost along with oldName.
//
// A oneOf or anyOf that listed both schemas lists newName twice afterwards, so
// it keeps only the first of those plain references: a value matching one
// would match the other, and a oneOf could never hold for it.
//
// It fails, changing nothing, if oldName or newName is not in
// components.schemas ([ErrSchemaNotFound]).
//
// [openapi-merge]: https://github.com/MarkRosemaker/openapi-merge
func RedirectSchema(doc *openapi.Document, oldName, newName, description string) error {
	return redirectSchemas(doc, map[string]string{oldName: newName}, description)
}

// RedirectSchemas is [RedirectSchema] for many schemas at once: it repoints every reference to each key of to at that
// key's value, and removes the keys from components.schemas.
//
// It walks the document a fixed number of times however many schemas it redirects, where calling RedirectSchema for
// each would walk it once per schema.
//
// A key redirected onto itself is left alone. Otherwise it fails, changing nothing, if a key or a value is not in
// components.schemas, or if a value is itself redirected and so would not be there afterwards ([ErrSchemaNotFound]).
func RedirectSchemas(doc *openapi.Document, to map[string]string) error {
	return redirectSchemas(doc, to, "")
}

func redirectSchemas(doc *openapi.Document, to map[string]string, description string) error {
	schemas := doc.Components.Schemas

	for _, oldName := range slices.Sorted(maps.Keys(to)) {
		if _, ok := schemas[oldName]; !ok {
			return &ErrSchemaNotFound{Name: oldName}
		}

		newName := to[oldName]
		next, redirected := to[newName]
		if _, ok := schemas[newName]; !ok || redirected && next != newName {
			return &ErrSchemaNotFound{Name: newName}
		}
	}

	to = withoutUnchanged(to)
	if len(to) == 0 {
		return nil
	}

	addImplicitMappings := implicitMappings(doc, to)

	repointed := map[*openapi.Schema]bool{}

	WalkSchemas(doc, func(s *openapi.Schema) {
		newName, ok := renamedRef(s, to)
		if !ok {
			return
		}

		if description != "" {
			s.Description = description
		}

		s.Ref.Identifier, s.Ref.Value = schemaRefPrefix+newName, schemas[newName]
		repointed[s] = true
	})

	WalkSchemas(doc, func(s *openapi.Schema) {
		s.OneOf = dropDuplicateAlternatives(s.OneOf, repointed)
		s.AnyOf = dropDuplicateAlternatives(s.AnyOf, repointed)
	})

	rewriteMappings(doc, to)
	addImplicitMappings()

	for oldName := range to {
		delete(schemas, oldName)
	}

	return nil
}

// dropDuplicateAlternatives keeps only the first of a union's alternatives that refer to the same schema and nothing
// else, once redirecting has made more than one of them do so.
//
// Two alternatives of the same schema are no alternative at all: a value that matches one matches the other, so a
// oneOf could never hold for it.
func dropDuplicateAlternatives(alts openapi.SchemaList, repointed map[*openapi.Schema]bool) openapi.SchemaList {
	var refs []string
	for _, a := range alts {
		if repointed[a] && !slices.Contains(refs, a.Ref.Identifier) {
			refs = append(refs, a.Ref.Identifier)
		}
	}

	for _, ref := range refs {
		alts = dropDuplicateRefs(alts, ref)
	}

	return alts
}

// dropDuplicateRefs keeps only the first of alts that refers to ref and nothing else.
func dropDuplicateRefs(alts openapi.SchemaList, ref string) openapi.SchemaList {
	isRef := func(a *openapi.Schema) bool {
		if a.Ref == nil || a.Ref.Identifier != ref {
			return false
		}

		c := *a
		c.Ref, c.Title, c.Description = nil, "", ""
		b, err := json.Marshal(&c)

		return err == nil && string(b) == "{}"
	}

	i := slices.IndexFunc(alts, isRef)
	if i < 0 {
		return alts
	}

	first := alts[i]

	return slices.DeleteFunc(alts, func(a *openapi.Schema) bool { return a != first && isRef(a) })
}
