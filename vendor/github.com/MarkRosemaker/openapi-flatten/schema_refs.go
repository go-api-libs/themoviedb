package flatten

import (
	"regexp"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

func inlineSchemas(d *openapi.Document, ss openapi.Schemas, prefix string) error {
	for name, s := range ss.ByIndex() {
		if err := inlineSchema(d, s, propertyName(d, s, prefix, name), false); err != nil {
			return &errpath.ErrKey{Key: name, Err: err}
		}
	}

	return nil
}

// propertyName is the name of s, what the property name of the schema named prefix holds: the property's own, as
// MotionAdjustments for motion_adjustments, where it says what s is in two words or more and no schema has it, or
// else prefixed, as JobData for data, which says little alone. A union of values keeps the prefix too: what its
// property says, as has_unpublished_changes or last_run_at, is what the value means, not what kind of thing it is.
func propertyName(d *openapi.Document, s *openapi.Schema, prefix, name string) string {
	own := goName(name)
	if _, taken := d.Components.Schemas[own]; !taken && reWords.MatchString(name) && !isUnion(s) {
		return own
	}

	return joinName(prefix, name)
}

// isUnion reports whether s, or what s holds where it may also be null, is a oneOf or anyOf of alternatives.
func isUnion(s *openapi.Schema) bool {
	if alt := nonNull(s); alt != nil {
		s = alt
	}

	return len(s.OneOf) > 0 || len(s.AnyOf) > 0
}

// reWords matches a property name of two words or more, as motion_adjustments, north-east or motionAdjustments, not
// an ID such as CasdiSFqTw7i8FtJGuQyFEq0.
var reWords = regexp.MustCompile(`^[a-z][a-z0-9]*([_-][a-z][a-z0-9]*)+$|^[a-z]+([A-Z][a-z]+)+$`)
