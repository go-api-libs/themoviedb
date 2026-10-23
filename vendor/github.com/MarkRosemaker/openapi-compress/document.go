package compress

import (
	"cmp"
	"encoding/json/v2"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/MarkRosemaker/openapi"
	"github.com/MarkRosemaker/openapi-compare/schema"
	edit "github.com/MarkRosemaker/openapi-edit"
)

// Document compresses an OpenAPI document so it contains no duplicate schemas.
// It does so by merging schemas that have the same shape (see schema.SameShape) -
// documentation-only differences like title or description don't prevent a merge.
// Furthermore, schemas with significant overlap are merged according to cfg.
// After compression, long names of merged schemas are shortened.
func Document(d *openapi.Document, cfg Config) error {
	cfg.setDefaults()

	if err := cfg.validate(); err != nil {
		return err
	}

	// read before comparing anything, since schemas with different extensions have different shapes
	derived := takeOrigins(d)

	deduplicateParameters(d)

	// Step down from exact equality to MinSimilarity, running each threshold
	// until stable before moving to the next.
	mergedCanonicals := map[string]bool{}
	threshold := 1.0
	for {
		for {
			canonicals, err := deduplicateSchemasAtThreshold(d, threshold, derived)
			if err != nil {
				return err
			}

			if len(canonicals) == 0 {
				break
			}

			for name := range canonicals {
				mergedCanonicals[name] = true
			}
		}

		if threshold <= cfg.MinSimilarity+1e-9 {
			break
		}

		threshold = math.Max(cfg.MinSimilarity, threshold-cfg.SimilarityStep)
	}

	// merging schemas can make parameters that referred to different ones identical
	deduplicateParameters(d)

	if !cfg.SkipNameShortening {
		if err := shortenMergedSchemaNames(d, mergedCanonicals); err != nil {
			return err
		}
	}

	if cfg.TrimExamples > 0 {
		if err := edit.TrimSchemaExamples(d, cfg.TrimExamples); err != nil {
			return err
		}
	}

	return nil
}

// deduplicateSchemasAtThreshold performs one dedup pass at the given similarity
// threshold.  It returns the set of canonical schema names that had at least one
// other schema merged into them (empty map means nothing was merged).
func deduplicateSchemasAtThreshold(d *openapi.Document, threshold float64, derived map[string]bool) (map[string]bool, error) {
	schemas := d.Components.Schemas
	if len(schemas) < 2 {
		return nil, nil
	}

	// a bare scalar carries nothing but its name and documentation, which merging would erase
	names := slices.DeleteFunc(sortedSchemaNames(schemas), func(name string) bool {
		return isBareScalar(schemas[name])
	})

	// The first of schemas that merge is the one kept: one the specification named rather than one flatten did, else
	// the one with the most references, else the shortest name.
	refs := edit.CountReferences(d)
	slices.SortStableFunc(names, func(a, b string) int {
		return cmp.Or(compareBool(derived[a], derived[b]), cmp.Compare(refs[b], refs[a]), cmp.Compare(len(a), len(b)))
	})

	// the loop below runs over every pair, so it looks schemas up by position rather than by name
	list := make([]*openapi.Schema, len(names))
	keys := make([]string, len(names))
	for i, name := range names {
		list[i] = schemas[name]
		keys[i] = shapeKey(list[i])
	}

	removed := make([]bool, len(names))

	// replacements maps a name-to-remove to its canonical name.
	replacements := map[string]string{}

	for i, schemaA := range list {
		if removed[i] {
			continue
		}

		for j := i + 1; j < len(list); j++ {
			if removed[j] {
				continue
			}

			schemaB := list[j]

			sim, ok := similarity(schemaA, schemaB, keys[i] == keys[j], threshold)
			if !ok {
				continue
			}

			if sim < 1.0 {
				// Not exactly equal: widen schemaA to also cover schemaB.
				mergeSchemas(schemaA, schemaB)
			} else {
				intersectRequired(schemaA, schemaB)
			}

			fillExamples(schemaA, schemaB)

			removed[j] = true
			replacements[names[j]] = names[i]
		}
	}

	if len(replacements) == 0 {
		return nil, nil
	}

	// Collect canonical names (the values in replacements).
	canonicals := make(map[string]bool, len(replacements))
	for _, canonical := range replacements {
		canonicals[canonical] = true
	}

	if err := edit.DescribeReferences(d, describeWhereUsed(schemas, replacements)); err != nil {
		return nil, err
	}

	if err := edit.RedirectSchemas(d, replacements); err != nil {
		return nil, err
	}

	return canonicals, nil
}

// similarity scores schemas a and b, whose shape keys are equal or not, and reports whether they are similar enough
// to merge at threshold.
func similarity(a, b *openapi.Schema, sameKey bool, threshold float64) (float64, bool) {
	if threshold >= 1.0 {
		// Fast path: same shape, ignoring documentation-only differences.
		return 1.0, sameKey && schema.SameShape(a, b)
	}

	// only two objects with properties can be similar without having the same shape
	if !sameKey && (!hasProperties(a) || !hasProperties(b)) {
		return 0, false
	}

	// Every property in only one schema scores nothing, so if the shared names alone are too few, skip the expensive
	// similarity computation.
	if pa, pb := len(a.Properties), len(b.Properties); pa > 0 && pb > 0 {
		shared := sharedNames(a.Properties, b.Properties)
		if float64(shared)/float64(pa+pb-shared) < threshold {
			return 0, false
		}
	}

	sim := schemasSimilarity(a, b)

	return sim, sim >= threshold
}

// deduplicateParameters removes exact duplicate parameter definitions from
// d.Components.Parameters, keeping the alphabetically-first name as canonical
// and updating all $ref identifiers throughout the document.
func deduplicateParameters(d *openapi.Document) {
	params := d.Components.Parameters
	if len(params) < 2 {
		return
	}

	names := sortedParameterNames(params)
	replacements := map[string]string{}

	for i, nameA := range names {
		if _, removed := replacements[nameA]; removed {
			continue
		}

		refA := params[nameA]
		if refA == nil || refA.Value == nil {
			continue
		}

		for _, nameB := range names[i+1:] {
			if _, removed := replacements[nameB]; removed {
				continue
			}

			refB := params[nameB]
			if refB == nil || refB.Value == nil {
				continue
			}

			if reflect.DeepEqual(refA.Value, refB.Value) {
				replacements[nameB] = nameA
			}
		}
	}

	if len(replacements) == 0 {
		return
	}

	for name := range replacements {
		delete(d.Components.Parameters, name)
	}

	replaceParameterRefsInDocument(d, replacements)
}

func sortedParameterNames(params openapi.Parameters) []string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// replaceParameterRefsInDocument updates $ref identifiers for parameters
// throughout the entire document.
func replaceParameterRefsInDocument(d *openapi.Document, replacements map[string]string) {
	for _, p := range d.Paths {
		replaceParameterRefsInPathItem(p, replacements)
	}

	for _, piRef := range d.Webhooks {
		if piRef != nil && piRef.Value != nil {
			replaceParameterRefsInPathItem(piRef.Value, replacements)
		}
	}

	for _, piRef := range d.Components.PathItems {
		if piRef != nil && piRef.Value != nil {
			replaceParameterRefsInPathItem(piRef.Value, replacements)
		}
	}

	replaceParameterRefsInCallbacks(d.Components.Callbacks, replacements)
}

func replaceParameterRefsInPathItem(p *openapi.PathItem, replacements map[string]string) {
	if p == nil {
		return
	}

	replaceParameterRefList(p.Parameters, replacements)

	for _, op := range p.Operations {
		if op != nil {
			replaceParameterRefList(op.Parameters, replacements)
			replaceParameterRefsInCallbacks(op.Callbacks, replacements)
		}
	}
}

// replaceParameterRefsInCallbacks updates $ref identifiers for parameters in the requests the callbacks describe.
func replaceParameterRefsInCallbacks(cs openapi.CallbackRefs, replacements map[string]string) {
	for _, c := range cs {
		if c == nil || c.Value == nil {
			continue
		}

		for _, piRef := range *c.Value {
			if piRef != nil {
				replaceParameterRefsInPathItem(piRef.Value, replacements)
			}
		}
	}
}

func replaceParameterRefList(params openapi.ParameterList, replacements map[string]string) {
	for _, p := range params {
		if p == nil || p.Ref == nil {
			continue
		}

		name := parameterNameFromRef(p.Ref.Identifier)
		if canonical, ok := replacements[name]; ok {
			p.Ref.Identifier = "#/components/parameters/" + canonical
		}
	}
}

func parameterNameFromRef(identifier string) string {
	const prefix = "#/components/parameters/"
	return strings.TrimPrefix(identifier, prefix)
}

func sortedSchemaNames(schemas openapi.Schemas) []string {
	names := make([]string, 0, len(schemas))
	for name := range schemas {
		names = append(names, name)
	}

	sort.Strings(names)

	return names
}

// isBareScalar reports whether s is a string, number, integer or boolean with no constraint of its own: nothing but a
// type and documentation, such as a component named idRequest that is only {"type": "string"}.
func isBareScalar(s *openapi.Schema) bool {
	switch s.Type {
	case openapi.TypeString, openapi.TypeNumber, openapi.TypeInteger, openapi.TypeBoolean:
	default:
		return false
	}

	c := *s
	c.Type, c.Title, c.Description, c.Deprecated = "", "", "", false
	c.Default, c.Example, c.Examples, c.Extensions = nil, nil, nil, nil

	b, err := json.Marshal(&c)

	return err == nil && string(b) == "{}"
}

// shapeKey is the same for any two schemas of the same shape (see schema.SameShape), and cheap to compare.
func shapeKey(s *openapi.Schema) string {
	var b strings.Builder

	b.WriteString(string(s.Type))

	if s.Ref != nil {
		b.WriteString(s.Ref.Identifier)
	}

	for _, name := range sortedSchemaNames(s.Properties) {
		b.WriteString("|" + name)
	}

	return b.String()
}

func hasProperties(s *openapi.Schema) bool {
	return s.Type == openapi.TypeObject && len(s.Properties) > 0
}

// sharedNames counts the property names a and b have in common.
func sharedNames(a, b openapi.Schemas) int {
	if len(a) > len(b) {
		a, b = b, a
	}

	n := 0

	for name := range a {
		if _, ok := b[name]; ok {
			n++
		}
	}

	return n
}

// describeWhereUsed returns the description of each schema merging into another, or being merged into, wherever the
// schemas merging into one disagree on it, and takes it off the schema kept.
//
// A description says what a schema is used for in one place, not what shape it has, so after the merge it belongs
// beside the references to each schema rather than on the one kept, where every reference would show it.
func describeWhereUsed(schemas openapi.Schemas, replacements map[string]string) map[string]string {
	groups := map[string][]string{}
	for name, canonical := range replacements {
		groups[canonical] = append(groups[canonical], name)
	}

	describe := map[string]string{}

	for canonical, merged := range groups {
		members := append(merged, canonical)

		desc := schemas[canonical].Description
		if !slices.ContainsFunc(members, func(name string) bool { return schemas[name].Description != desc }) {
			continue
		}

		for _, name := range members {
			if d := schemas[name].Description; d != "" {
				describe[name] = d
			}
		}

		schemas[canonical].Description = ""
	}

	return describe
}
