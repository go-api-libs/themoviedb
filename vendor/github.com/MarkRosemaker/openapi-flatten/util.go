package flatten

import (
	"fmt"
	"strings"

	"github.com/ettle/strcase"
)

// uniqueName is name, made a valid component name, or else that with the lowest number after it that m has no entry
// for.
func uniqueName[M ~map[string]V, V any](m M, name string) string {
	name = componentName(name)
	idx := 1
	altName := name

	for {
		// check if the name already exists
		if _, ok := m[altName]; !ok {
			return altName
		}

		// increase the number next to the name
		idx++
		altName = fmt.Sprintf("%s%d", name, idx)
	}
}

// componentName is name without what a component name may not hold: anything but ASCII letters, digits, '.', '-' and
// '_', as the braces of a path template in an operation ID or the padding of a base64 property key. Where nothing is
// left, it is "Unnamed".
func componentName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case 'a' <= r && r <= 'z', 'A' <= r && r <= 'Z', '0' <= r && r <= '9', r == '.', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, name)

	if name == "" {
		return "Unnamed"
	}

	return name
}

// goName is the words of parts as one Go name, for a component flatten names after an operation, a property or a
// title. Anything but an ASCII letter or digit breaks words, as the braces of a path template in an operation ID do,
// so get_a-{v} is GetAV rather than a name that runs the parameter into the next word.
func goName(parts ...string) string {
	return strcase.ToGoPascal(strings.Map(func(r rune) rune {
		if 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9' {
			return r
		}

		return ' '
	}, strings.Join(parts, " ")))
}
