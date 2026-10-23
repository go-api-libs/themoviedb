package compress

import (
	"maps"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/MarkRosemaker/openapi"
	edit "github.com/MarkRosemaker/openapi-edit"
)

// methodWords begin a name derived from an operation, as its HTTP method; anywhere else, such as the Post of
// GetPostByID, they name what the API is about.
var methodWords = map[string]bool{
	"Get": true, "Post": true, "Put": true, "Delete": true,
	"Patch": true, "Head": true, "Options": true, "Trace": true,
}

// noiseWords are removed when shortening path-derived schema names.
// They are structural or protocol-level tokens that carry no domain meaning.
var noiseWords = map[string]bool{
	// HTTP/REST wrappers
	"Ok": true, "Response": true, "Request": true, "Body": true,
	// Format indicators
	"JSON": true, "Json": true, "XML": true, "Xml": true,
}

// splitCamelCase splits a CamelCase/PascalCase identifier into its component
// words.  Consecutive uppercase letters are treated as an abbreviation so that
// "JSONResponse" → ["JSON", "Response"] rather than individual letters.
// Digit → upper-letter transitions are also split: "Cik0000320193JSON" →
// ["Cik0000320193", "JSON"].
func splitCamelCase(s string) []string {
	runes := []rune(s)

	n := len(runes)
	if n == 0 {
		return nil
	}

	var words []string

	start := 0
	for i := 1; i < n; i++ {
		if unicode.IsUpper(runes[i]) {
			if unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) {
				// lower/digit → upper boundary: "petId", "0093JSON" → split
				words = append(words, string(runes[start:i]))
				start = i
			} else if i+1 < n && unicode.IsLower(runes[i+1]) {
				// consecutive-upper → lower boundary: "JSONResponse" → split before 'R'
				words = append(words, string(runes[start:i]))
				start = i
			}
		}
	}

	words = append(words, string(runes[start:]))

	return words
}

// splitAlphaDigit splits a word at letter/digit boundaries.
// "Cik0000320193" → ["Cik", "0000320193"].
func splitAlphaDigit(w string) []string {
	if w == "" {
		return nil
	}

	runes := []rune(w)

	var parts []string

	start := 0
	prevDigit := unicode.IsDigit(runes[0])
	for i := 1; i < len(runes); i++ {
		currDigit := unicode.IsDigit(runes[i])
		if currDigit != prevDigit {
			parts = append(parts, string(runes[start:i]))
			start = i
			prevDigit = currDigit
		}
	}

	return append(parts, string(runes[start:]))
}

// isAllDigits reports whether every rune in s is a decimal digit.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}

	return true
}

// isVersionSegment reports whether w looks like a version token (V1, V2, v3 …).
func isVersionSegment(w string) bool {
	if len(w) < 2 || (w[0] != 'V' && w[0] != 'v') {
		return false
	}

	for _, r := range w[1:] {
		if !unicode.IsDigit(r) {
			return false
		}
	}

	return true
}

// shortName computes a shortened version of name that is not already in
// existing.  It removes noise words, an HTTP method it begins with, version segments, and digit-only tokens
// (e.g. path parameters like CIK numbers), deduplicates consecutive identical
// words (case-insensitive), then ensures uniqueness.
// Returns name unchanged if no meaningful shortening is possible.
func shortName(name string, existing openapi.Schemas) string {
	words := splitCamelCase(name)

	filtered := make([]string, 0, len(words)*2)
	for i, w := range words {
		if noiseWords[w] || isVersionSegment(w) || i == 0 && methodWords[w] {
			continue
		}
		// Further split at letter/digit boundaries and drop digit-only tokens.
		// This strips embedded path parameters such as "Cik0000320193" → "Cik".
		for _, sub := range splitAlphaDigit(w) {
			if !isAllDigits(sub) {
				filtered = append(filtered, sub)
			}
		}
	}

	if len(filtered) == 0 {
		return name
	}

	// Remove consecutive duplicates (case-insensitive).
	deduped := filtered[:1]
	for i := 1; i < len(filtered); i++ {
		if !strings.EqualFold(filtered[i], filtered[i-1]) {
			deduped = append(deduped, filtered[i])
		}
	}

	candidate := strings.Join(deduped, "")
	if candidate == name {
		return name
	}

	return uniqueName(candidate, name, existing)
}

// uniqueName returns candidate if it is not taken in existing (ignoring
// currentName, since that slot will be freed by the rename).  Otherwise it
// appends a numeric suffix until a free name is found.
func uniqueName(candidate, currentName string, existing openapi.Schemas) string {
	if _, ok := existing[candidate]; !ok || candidate == currentName {
		return candidate
	}

	for i := 2; ; i++ {
		n := candidate + strconv.Itoa(i)
		if _, ok := existing[n]; !ok {
			return n
		}
	}
}

// shortenMergedSchemaNames renames each schema in targets to a shorter name.
// targets is the set of schema names that acted as canonical during at least
// one merge pass.  Schemas that were themselves merged away are silently
// skipped.  Names are chosen in sorted order for determinism, each one seeing
// the names chosen before it, and the schemas are then renamed together.
func shortenMergedSchemaNames(d *openapi.Document, targets map[string]bool) error {
	names := make([]string, 0, len(targets))
	for name := range targets {
		names = append(names, name)
	}

	sort.Strings(names)

	existing := maps.Clone(d.Components.Schemas)
	to := map[string]string{}
	original := map[string]string{} // a name chosen here, to the schema's name in d

	for _, name := range names {
		s, ok := existing[name]
		if !ok {
			continue // merged away in a later pass
		}

		short := shortName(name, existing)
		if short == name {
			continue
		}

		from, renamed := original[name]
		if !renamed {
			from = name
		}

		delete(existing, name)
		existing[short] = s
		to[from] = short
		original[short] = from
	}

	return edit.RenameSchemas(d, to)
}
