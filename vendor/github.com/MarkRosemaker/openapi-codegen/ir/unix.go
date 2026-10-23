package ir

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// encodings are how a time.Time or civil.Date may be written other than as its type does it: as an integer of unix
// time, or, for a date, in ISO 8601's basic form, such as "20260320".
const (
	encodingUnix  = "integer"
	encodingBasic = "basic-form"
)

// encodingMode is how a document's values of one Go type in one encoding are written and read.
type encodingMode struct {
	// all is set when every value of the type is in it: a marshaler for the type writes them all.
	all bool
	// members are, where others are not, the names of the object members holding the values in it.
	members []string
}

// fieldEncoding is the encoding of f's time.Time or civil.Date, or "" for the type's own.
func fieldEncoding(f Field) string {
	switch {
	case f.IsUnix:
		return encodingUnix
	case f.IsDateBasic:
		return encodingBasic
	default:
		return ""
	}
}

// markEncodings decides, for time.Time and civil.Date, how the values written other than as their type does it are
// written and read: all of them by one marshaler, where they are the only ones, or else by the names of the members
// holding them. It returns, by Go type, the mode of each encoding there is.
func markEncodings(schemas []Schema) (map[string]map[string]encodingMode, error) {
	modes := map[string]map[string]encodingMode{}

	for _, goType := range []string{"time.Time", "civil.Date"} {
		names := map[string]map[string]bool{} // by encoding, "" for the type's own
		position := map[string]string{}       // by encoding, a tuple's field, which no member name reaches

		for _, s := range schemas {
			for _, f := range s.Fields {
				if f.Embedded || !containsType(f.Type, goType) || f.IsDateTimeOrInt {
					// a date-time or an integer reads either: it is written as the others are, or as a string
					continue
				}

				enc := fieldEncoding(f)

				switch {
				case enc != "" && s.Kind == SchemaKindTuple:
					position[enc] = s.Name + "." + f.Name
				case enc != "" && strings.Contains(f.Type, "map["):
					return nil, fmt.Errorf("%s.%s: a map of %s %s is not supported", s.Name, f.Name, enc, goType)
				default:
					if names[enc] == nil {
						names[enc] = map[string]bool{}
					}

					names[enc][f.JSONName] = true
				}
			}
		}

		var encs []string

		for _, enc := range []string{encodingUnix, encodingBasic} {
			if len(names[enc]) > 0 || position[enc] != "" {
				encs = append(encs, enc)
			}
		}

		if len(encs) == 0 {
			continue
		}

		modes[goType] = map[string]encodingMode{}

		if len(encs) == 1 && len(names[""]) == 0 {
			modes[goType][encs[0]] = encodingMode{all: true}
			continue
		}

		for _, enc := range encs {
			if p := position[enc]; p != "" {
				return nil, fmt.Errorf("%s: a %s %s among other forms is not supported in a tuple, which has no member names", p, enc, goType)
			}

			for name := range names[enc] {
				for other := range names {
					if other != enc && names[other][name] {
						return nil, fmt.Errorf("member %q holds a %s %s in one schema and another form in another, which its name cannot tell apart", name, enc, goType)
					}
				}

				if strings.Trim(name, "0123456789") == "" {
					return nil, fmt.Errorf("member %q: a %s %s among other forms needs a name that is no number", name, enc, goType)
				}
			}

			modes[goType][enc] = encodingMode{members: slices.Sorted(maps.Keys(names[enc]))}
		}
	}

	return modes, nil
}
