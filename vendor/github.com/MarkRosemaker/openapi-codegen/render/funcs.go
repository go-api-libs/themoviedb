package render

import (
	"slices"
	"strings"
	"text/template"
	"unicode"

	"github.com/ettle/strcase"
)

// templateFuncs returns the function map available to all templates.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"lower":      strings.ToLower,
		"upper":      strings.ToUpper,
		"trimSpace":  strings.TrimSpace,
		"hasPrefix":  strings.HasPrefix,
		"hasSuffix":  strings.HasSuffix,
		"contains":   strings.Contains,
		"replace":    strings.ReplaceAll,
		"join":       strings.Join,
		"isExported": isExported,
		"isKeyword":  isGoKeyword,
		"deref":      deref,
		"add":        func(a, b int) int { return a + b },
		"sub":        func(a, b int) int { return a - b },
		"last":       func(i, n int) bool { return i == n-1 },
		"list":       func(v ...any) []any { return v },
		"titleCase": func(s string) string {
			if s == "" {
				return s
			}

			r := []rune(strings.ToLower(s))
			r[0] = unicode.ToUpper(r[0])

			return string(r)
		},
		"camelCase":   strcase.ToGoCamel,
		"typeZeroVal": typeZeroVal,
		"toGoComment": toGoComment,
	}
}

// isExported reports whether the identifier starts with an uppercase letter.
func isExported(s string) bool {
	r := []rune(s)
	return len(r) > 0 && unicode.IsUpper(r[0])
}

// isGoKeyword reports whether s is a reserved Go keyword.
func isGoKeyword(s string) bool {
	keywords := []string{
		"break", "case", "chan", "const", "continue",
		"default", "defer", "else", "fallthrough", "for",
		"func", "go", "goto", "if", "import",
		"interface", "map", "package", "range", "return",
		"select", "struct", "switch", "type", "var",
	}

	return slices.Contains(keywords, s)
}

// deref returns the pointer base type, e.g. "*Foo" → "Foo".
func deref(s string) string {
	return strings.TrimPrefix(s, "*")
}

// typeZeroVal returns the Go zero-value literal for a Go type string.
func typeZeroVal(goType string) string {
	switch goType {
	case "string", "types.Email":
		return `""`
	case "bool":
		return "false"
	case "int", "int32", "int64", "uint", "uint32", "uint64", "float32", "float64", "time.Duration":
		return "0"
	case "uuid.UUID":
		return "uuid.Nil()"
	case "url.URL":
		return "url.URL{}"
	case "time.Time":
		return "time.Time{}"
	case "civil.Date":
		return "civil.Date{}"
	case "net.IP":
		return "nil"
	default:
		return `""`
	}
}

func toGoComment(in string) string {
	in = strings.TrimSpace(in)
	if len(in) == 0 { // ignore empty comment
		return ""
	}

	// Normalize newlines from Windows/Mac to Linux
	in = strings.ReplaceAll(in, "\r\n", "\n")
	in = strings.ReplaceAll(in, "\r", "\n")

	lines := dedent(strings.Split(in, "\n"))

	// Add comment to each line
	for i, line := range lines {
		lines[i] = "// " + line
	}

	return strings.Join(lines, "\n")
}

// dedent removes the indentation lines after the first share, as a docstring has it, so that it does not become a
// code block in the Go doc comment. Lines indented beyond the rest keep the difference.
func dedent(lines []string) []string {
	common := -1
	for _, line := range lines[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}

		if indent := len(line) - len(trimmed); common < 0 || indent < common {
			common = indent
		}
	}

	if common <= 0 {
		return lines
	}

	for i := 1; i < len(lines); i++ {
		if len(lines[i]) >= common {
			lines[i] = lines[i][common:]
		} else {
			lines[i] = strings.TrimLeft(lines[i], " \t")
		}
	}

	return lines
}
