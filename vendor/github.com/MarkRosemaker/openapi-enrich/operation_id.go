package enrich

import (
	"net/http"
	"slices"
	"strings"

	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

// inferOperationID returns an operation ID for the given HTTP method and path.
//
// Rules:
//   - Static segments are added in PascalCase.
//   - A static segment immediately before a param segment is singularized.
//   - An intermediate param segment (not the last) is skipped entirely.
//   - The last param segment becomes "By{ParamName}".
//   - The leading verb depends on method and whether the last segment is a param:
//     GET  + last=param  → "Get"
//     GET  + last=static → "List"
//     POST → "Post", PUT → "Put", PATCH → "Patch", DELETE → "Delete", etc.
//
// Examples:
//
//	GET  /users                        → ListUsers
//	GET  /users/{id}                   → GetUserByID
//	GET  /users/{userId}/posts         → ListUserPosts
//	GET  /users/{userId}/posts/{postId}→ GetUserPostByPostID
//	POST /users                        → PostUsers
//	PUT  /users/{id}                   → PutUserByID
func inferOperationID(method string, path openapi.Path) string {
	segments := strings.Split(strings.Trim(string(path), "/"), "/")

	lastParamIdx := -1
	for i, segment := range slices.Backward(segments) {
		if isParamSegment(segment) {
			lastParamIdx = i
			break
		}
	}

	lastIsParam := lastParamIdx == len(segments)-1

	var verb string
	switch strings.ToUpper(method) {
	case http.MethodGet:
		if lastIsParam {
			verb = "Get"
		} else {
			verb = "List"
		}
	case http.MethodPost:
		verb = "Post"
	case http.MethodPut:
		verb = "Put"
	case http.MethodPatch:
		verb = "Patch"
	case http.MethodDelete:
		verb = "Delete"
	case http.MethodHead:
		verb = "Head"
	case http.MethodOptions:
		verb = "Options"
	default:
		verb = strcase.ToGoPascal(strings.ToLower(method))
	}

	var parts []string

	parts = append(parts, verb)

	for i, seg := range segments {
		if isParamSegment(seg) {
			// Intermediate param: skip. Last param: add "By{ParamName}".
			if i == len(segments)-1 {
				paramName := seg[1 : len(seg)-1]
				parts = append(parts, "By"+strcase.ToGoPascal(paramName))
			}

			continue
		}

		if seg == "" {
			continue
		}

		// Embedded-param segment like "CIK{cik}.json": expand each token separately.
		if strings.Contains(seg, "{") {
			parts = append(parts, expandEmbeddedSegment(seg)...)
			continue
		}

		// Static segment: singularize if it is directly followed by a param.
		nextIsParam := i+1 < len(segments) && isParamSegment(segments[i+1])
		if nextIsParam {
			parts = append(parts, strcase.ToGoPascal(singularize(seg)))
		} else {
			parts = append(parts, strcase.ToGoPascal(seg))
		}
	}

	return strings.Join(parts, "")
}

func isParamSegment(seg string) bool {
	return len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}'
}

// expandEmbeddedSegment converts a segment like "CIK{cik}.json" into PascalCase
// parts ["Cik", "Cik", "JSON"] by splitting at { and } boundaries.
func expandEmbeddedSegment(seg string) []string {
	var parts []string
	for seg != "" {
		start := strings.Index(seg, "{")
		if start < 0 {
			if p := segmentTokenToPascal(seg); p != "" {
				parts = append(parts, p)
			}
			break
		}

		if start > 0 {
			if p := segmentTokenToPascal(seg[:start]); p != "" {
				parts = append(parts, p)
			}
		}

		end := strings.Index(seg[start:], "}")
		if end < 0 {
			break
		}

		end += start
		if paramName := seg[start+1 : end]; paramName != "" {
			parts = append(parts, strcase.ToGoPascal(paramName))
		}

		seg = seg[end+1:]
	}

	return parts
}

// segmentTokenToPascal strips leading punctuation (e.g. ".") and converts to PascalCase.
func segmentTokenToPascal(s string) string {
	s = strings.TrimLeft(s, ".-_")
	if s == "" {
		return ""
	}

	return strcase.ToGoPascal(s)
}
