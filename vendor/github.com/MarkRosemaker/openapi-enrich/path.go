package enrich

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// pathElement is a segment of a URL path — either a static name or a parameter.
// For embedded parameters like "CIK{cik}.json", prefix="CIK" and suffix=".json".
type pathElement struct {
	name    string // static segment name or parameter name
	isParam bool
	prefix  string // non-empty only for embedded params
	suffix  string // non-empty only for embedded params
}

// parsedPath is a parsed URL path template.
type parsedPath []*pathElement

// parsePath splits a path like "/users/{id}/posts" or "/companyfacts/CIK{cik}.json" into elements.
func parsePath(p string) parsedPath {
	segments := strings.Split(strings.Trim(p, "/"), "/")
	if len(segments) == 1 && segments[0] == "" {
		return parsedPath{{name: "", isParam: false}}
	}

	result := make(parsedPath, 0, len(segments))
	for _, seg := range segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			// Whole-segment parameter: {id}
			result = append(result, &pathElement{name: seg[1 : len(seg)-1], isParam: true})
		} else if start := strings.Index(seg, "{"); start >= 0 {
			// Embedded parameter: prefix{name}suffix
			if end := strings.Index(seg[start:], "}"); end >= 0 {
				end += start
				result = append(result, &pathElement{
					name:    seg[start+1 : end],
					isParam: true,
					prefix:  seg[:start],
					suffix:  seg[end+1:],
				})

				continue
			}

			result = append(result, &pathElement{name: seg, isParam: false})
		} else {
			result = append(result, &pathElement{name: seg, isParam: false})
		}
	}

	return result
}

// fits reports whether the given URL path segments are compatible with this parsed path.
// segments are the actual request URL segments (no braces).
//
// A whole-segment parameter ({name}) in the last position is treated as greedy:
// it absorbs all remaining segments, allowing parameter values that contain
// slashes (e.g. {path} matching "github.com/google/go-cmp/cmp").
func (pp parsedPath) fits(segments []string) bool {
	if len(pp) == 0 {
		return len(segments) == 0
	}

	// A trailing whole-segment param is greedy — it can match multiple segments.
	last := pp[len(pp)-1]
	lastIsGreedy := last.isParam && last.prefix == "" && last.suffix == ""

	if lastIsGreedy {
		// The non-greedy prefix must fit, and there must be at least one segment
		// left for the greedy param to consume.
		if len(segments) < len(pp) {
			return false
		}
		// If there are extra segments beyond the template length, only absorb them
		// if the first consumed segment is NOT a single-segment ID (UUID or integer).
		// A UUID/integer value can't span multiple URL segments, so extra segments
		// beyond it indicate a more-specific sub-route that should get its own path.
		if len(segments) > len(pp) && looksLikeID(segments[len(pp)-1]) {
			return false
		}
	} else {
		if len(pp) != len(segments) {
			return false
		}
	}

	// Validate every element up to (but not including) the greedy last param.
	limit := len(pp)
	if lastIsGreedy {
		limit = len(pp) - 1
	}

	for i := 0; i < limit; i++ {
		el := pp[i]
		if el.isParam {
			if el.prefix != "" || el.suffix != "" {
				// Embedded param: segment must start with prefix and end with suffix.
				seg := segments[i]
				if !strings.HasPrefix(seg, el.prefix) || !strings.HasSuffix(seg, el.suffix) {
					return false
				}
				// The middle (variable) part must be non-empty.
				if len(seg)-len(el.prefix)-len(el.suffix) <= 0 {
					return false
				}
			}

			continue // whole-segment param: any single segment fits
		}

		if el.name != segments[i] {
			return false
		}
	}

	return true
}

// literalCount returns the number of non-parameter segments in pp, used to
// rank competing matches the way net/http's ServeMux prefers the pattern
// with more fixed segments.
func (pp parsedPath) literalCount() int {
	n := 0
	for _, el := range pp {
		if !el.isParam {
			n++
		}
	}

	return n
}

// pathCandidate is a template that matches a request, scored so the best of
// several candidates can be picked deterministically instead of depending on
// map iteration order.
type pathCandidate struct {
	path    openapi.Path
	pi      *openapi.PathItem
	exact   bool // same segment count as the request; no greedy absorption needed
	literal int
}

// better reports whether c should replace the current best candidate other.
// An exact-length match always beats one reached only by a trailing greedy
// param absorbing extra segments (see parsedPath.fits): that greedy
// absorption exists for genuine multi-segment values like a package import
// path, not to stand in for a same-length template that fits the request
// precisely. Within the same tier, more literal segments wins, and ties are
// broken by the path string for a deterministic result.
func (c pathCandidate) better(other pathCandidate) bool {
	if c.exact != other.exact {
		return c.exact
	}

	if c.literal != other.literal {
		return c.literal > other.literal
	}

	return c.path < other.path
}

// findPathItem finds an existing PathItem in doc.Paths that matches reqURL,
// after stripping the server base URL. It returns the matched path key and PathItem.
// If no match is found, it returns the relative path and nil.
func findPathItem(doc *openapi.Document, reqURL *url.URL) (openapi.Path, *openapi.PathItem) {
	if len(doc.Servers) == 0 {
		return "", nil
	}

	relPath := relativePath(reqURL, doc.Servers[0].URL)

	// 1. Exact match
	if pi, ok := doc.Paths[openapi.Path(relPath)]; ok {
		return openapi.Path(relPath), pi
	}

	// 2. Parametric match: score every fitting template and keep the best.
	reqSegments := pathSegments(relPath)

	var best *pathCandidate
	for path, pi := range doc.Paths {
		pp := parsePath(string(path))
		if !pp.fits(reqSegments) {
			continue
		}

		c := pathCandidate{path: path, pi: pi, exact: len(pp) == len(reqSegments), literal: pp.literalCount()}
		if best == nil || c.better(*best) {
			best = &c
		}
	}

	if best != nil {
		return best.path, best.pi
	}

	// 3. No match
	return openapi.Path(relPath), nil
}

// relativePath strips the server base URL from reqURL to get the relative path.
func relativePath(reqURL *url.URL, serverURL string) string {
	base, err := url.Parse(serverURL)
	if err != nil {
		return reqURL.Path
	}

	rel := reqURL.Path
	if base.Path != "" && base.Path != "/" {
		rel = strings.TrimPrefix(rel, strings.TrimSuffix(base.Path, "/"))
	}

	if rel == "" {
		rel = "/"
	}

	return rel
}

// pathSegments splits a path string into its segments (no braces, no leading slash).
func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return []string{""}
	}

	return strings.Split(trimmed, "/")
}

// newParametricPath creates a new path template from a concrete URL path,
// detecting which segments look like IDs (integers or UUIDs) and replacing
// them with path parameters.
func newParametricPath(urlPath string) (openapi.Path, []string) {
	segments := pathSegments(urlPath)
	parts := make([]string, len(segments))

	var paramNames []string

	for i, seg := range segments {
		if looksLikeID(seg) {
			// Derive a parameter name from the previous segment (if any).
			name := deriveParamName(segments, i)
			parts[i] = "{" + name + "}"
			paramNames = append(paramNames, name)
		} else if prefix, paramName, suffix, ok := extractEmbeddedParam(seg); ok {
			parts[i] = prefix + "{" + paramName + "}" + suffix
			paramNames = append(paramNames, paramName)
		} else {
			parts[i] = seg
		}
	}

	return openapi.Path("/" + strings.Join(parts, "/")), paramNames
}

// looksLikeID reports whether a path segment looks like a dynamic identifier
// (integer, UUID, or very long random-looking token).
func looksLikeID(seg string) bool {
	if seg == "" {
		return false
	}

	if _, err := strconv.Atoi(seg); err == nil {
		return true
	}

	if isUUID(seg) {
		return true
	}

	return false
}

// extractEmbeddedParam detects segments like "CIK0000320193.json" that have an
// alphabetic prefix, a run of 4 or more digits, and an optional non-digit suffix.
// It returns (prefix, paramName, suffix, true) on a match; paramName is the
// lowercase prefix.
func extractEmbeddedParam(seg string) (prefix, paramName, suffix string, ok bool) {
	// Find leading alphabetic prefix (must be non-empty).
	i := 0
	for i < len(seg) && ((seg[i] >= 'a' && seg[i] <= 'z') || (seg[i] >= 'A' && seg[i] <= 'Z')) {
		i++
	}

	if i == 0 {
		return "", "", "", false
	}

	// Find the digit run immediately following the prefix (at least 4 digits).
	j := i
	for j < len(seg) && seg[j] >= '0' && seg[j] <= '9' {
		j++
	}

	if j-i < 4 {
		return "", "", "", false
	}

	return seg[:i], strings.ToLower(seg[:i]), seg[j:], true
}

// deriveParamName returns a parameter name for the segment at index i,
// using the previous segment (singularized) as a prefix if available.
func deriveParamName(segments []string, i int) string {
	if i > 0 {
		prev := segments[i-1]
		// simple singularization: strip trailing 's'
		singular := singularize(prev)
		return singular + "Id"
	}

	return "id"
}

// singularize returns a naive singular form of a word.
func singularize(word string) string {
	if strings.HasSuffix(word, "ies") {
		return word[:len(word)-3] + "y"
	}

	if strings.HasSuffix(word, "ses") || strings.HasSuffix(word, "xes") || strings.HasSuffix(word, "zes") {
		return word[:len(word)-2]
	}

	if strings.HasSuffix(word, "s") && len(word) > 1 {
		return word[:len(word)-1]
	}

	return word
}
