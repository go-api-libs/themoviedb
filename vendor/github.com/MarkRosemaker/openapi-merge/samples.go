package merge

import (
	"encoding/json/jsontext"
	"encoding/json/v2"

	"github.com/MarkRosemaker/openapi"
)

// ExtensionSamples marks a union whose alternatives are samples of one value, such as the elements of a recorded
// array, rather than alternatives the value may take. Merged into a union, each sample goes into the alternative it
// matches; merged into a schema that is no union, each is merged into it in turn; merged into another union of
// samples, they join its own. The caller collapses what is left of it, as [Samples] does not describe the value.
const ExtensionSamples = "x-samples"

// Samples returns a union of samples of one value, marked by [ExtensionSamples].
func Samples(samples ...*openapi.Schema) *openapi.Schema {
	return &openapi.Schema{AnyOf: samples, Extensions: jsontext.Value(`{"` + ExtensionSamples + `":true}`)}
}

// IsSamples reports whether s is a union of samples, see [ExtensionSamples].
func IsSamples(s *openapi.Schema) bool {
	if s == nil || len(s.AnyOf) == 0 || len(s.Extensions) == 0 {
		return false
	}

	var ext map[string]jsontext.Value
	if err := json.Unmarshal(s.Extensions, &ext); err != nil {
		return false
	}

	_, ok := ext[ExtensionSamples]

	return ok
}

// mergeIfSamples merges b into a when either is a union of samples. handled reports whether it did.
func mergeIfSamples(a, b *openapi.Schema) (handled bool, err error) {
	switch {
	case IsSamples(a):
		if IsSamples(b) {
			a.AnyOf = append(a.AnyOf, b.AnyOf...)
		} else {
			a.AnyOf = append(a.AnyOf, b)
		}

		return true, nil
	case !IsSamples(b):
		return false, nil
	case len(a.OneOf) > 0 || len(a.AnyOf) > 0:
		// a union routes each sample into the alternative it matches
		return false, nil
	}

	for _, sample := range b.AnyOf {
		if err := Schema(a, deref(sample), false); err != nil {
			return true, err
		}
	}

	return true, nil
}
