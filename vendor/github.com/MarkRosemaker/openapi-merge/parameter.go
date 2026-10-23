package merge

import (
	"errors"
	"fmt"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// Parameter merges b into a, which must be in the same location: their names, descriptions, schemas, explode and
// examples. Parameters described by content are not supported yet.
func Parameter(a, b *openapi.Parameter) error {
	a.Name = mergeString(a.Name, b.Name)

	if a.In != b.In {
		return &errpath.ErrField{
			Field: "in",
			Err:   fmt.Errorf("%q vs. %q", a.In, b.In),
		}
	}

	// NOTE: a.Required, a.AllowEmptyValue, a.AllowReserved stay as is

	a.Description = mergeString(a.Description, b.Description)

	// A parameter MUST contain either a `schema` property, or a `content` property, but not both.
	switch {
	case b.Schema == nil:
		return errors.New("merging content unimplemented")
	case a.Schema == nil:
		return errors.New("merging content with schema unimplemented")
	}

	if err := Schema(deref(a.Schema), deref(b.Schema), true); err != nil {
		return &errpath.ErrField{Field: "schema", Err: err}
	}

	if deref(a.Schema).Type == openapi.TypeArray {
		// TODO: only delete if example is no longer an array
		a.Example = nil
		b.Example = nil
	}

	if a.Explode != nil || b.Explode != nil {
		if a.Explode == nil {
			a.Explode = b.Explode
		} else if b.Explode == nil {
			b.Explode = a.Explode
		} else if *a.Explode != *b.Explode {
			// true is default but one is not that, so set both to non-default
			no := false
			a.Explode = &no
			b.Explode = &no
		}
	}

	if a.Example == nil && a.Examples == nil {
		a.Example = b.Example
	}

	return extensions(a.Extensions, b.Extensions)
}
