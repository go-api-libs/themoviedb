package enrich

import "github.com/MarkRosemaker/openapi"

// NewDocument creates a minimal valid OpenAPI 3.1.0 document as a starting point.
func NewDocument() *openapi.Document {
	return &openapi.Document{
		OpenAPI: "3.1.0",
		Info: &openapi.Info{
			Title:   "API",
			Version: "0.0.1",
		},
	}
}
