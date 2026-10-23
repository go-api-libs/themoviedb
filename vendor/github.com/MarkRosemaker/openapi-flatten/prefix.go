package flatten

import (
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// moveCommonPathPrefix detects when all paths in the document share a common
// segment prefix (e.g. /v1) and moves that prefix into every server URL,
// shortening the paths accordingly.
//
// The check is skipped when there are fewer than two paths, or when the
// longest common prefix is the root ("/") — i.e. there is nothing meaningful
// to move.
func moveCommonPathPrefix(d *openapi.Document) {
	if len(d.Paths) < 2 {
		return
	}

	prefix := commonPathPrefix(d.Paths)
	if prefix == "" {
		return
	}

	// Strip the prefix from each path key, preserving insertion order.
	type entry struct {
		path openapi.Path
		item *openapi.PathItem
	}

	ordered := make([]entry, 0, len(d.Paths))
	for path, item := range d.Paths.ByIndex() {
		stripped := strings.TrimPrefix(string(path), prefix)
		if stripped == "" {
			stripped = "/"
		}

		ordered = append(ordered, entry{openapi.Path(stripped), item})
	}

	for path := range d.Paths {
		delete(d.Paths, path)
	}

	for _, e := range ordered {
		d.Paths.Set(e.path, e.item)
	}

	// Append the prefix to every server URL.
	for i := range d.Servers {
		d.Servers[i].URL = strings.TrimRight(d.Servers[i].URL, "/") + prefix
	}
}

// commonPathPrefix returns the longest common path prefix shared by all paths,
// measured in whole path segments (e.g. "/v1" for ["/v1/a", "/v1/b"]).
// Returns "" when only the root is common.
func commonPathPrefix(paths openapi.Paths) string {
	// Each path starts with "/", so splitting by "/" gives ["", seg1, seg2, ...].
	// The first element is always ""; we need at least one more shared segment.
	var common []string

	first := true
	for path := range paths {
		segs := strings.Split(string(path), "/")
		if first {
			common = segs
			first = false
			continue
		}

		common = sharedPrefix(common, segs)
		if len(common) <= 1 {
			return "" // only the leading empty string is shared
		}
	}

	if len(common) <= 1 {
		return ""
	}

	return strings.Join(common, "/")
}

// sharedPrefix returns the longest common prefix of two string slices.
func sharedPrefix(a, b []string) []string {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return a[:i]
		}
	}

	return a[:n]
}
