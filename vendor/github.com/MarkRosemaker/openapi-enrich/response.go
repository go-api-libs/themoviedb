package enrich

import (
	"fmt"
	"mime"
	"net/http"
	"strings"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi"
)

// buildResponse constructs an openapi.Response from an observed HTTP response.
func buildResponse(resp *cassette.Response) (*openapi.Response, error) {
	description := http.StatusText(resp.StatusCode)
	if description == "" {
		description = "response"
	}

	r := &openapi.Response{Description: description}

	ct := resp.Headers.Get("Content-Type")
	if ct == "" || len(resp.Body) == 0 && !resp.BodyOmitted {
		return r, nil
	}

	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return r, nil
	}

	switch {
	case !cassette.IsText(mediaType):
		// a zip, a PDF, an image or the like, which the recording does not keep
		r.Content = openapi.Content{}
		r.Content.Set(openapi.MediaRange(mediaType), &openapi.MediaType{Schema: binarySchema()})

	case isJSONMediaType(mediaType):
		schema, err := newSchemaFromJSON(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("creating schema from JSON: %w", err)
		}

		r.Content = openapi.Content{}
		r.Content.Set(openapi.MediaRange(mediaType), &openapi.MediaType{
			Schema: schema,
		})

	case mediaType == "text/plain":
		bodyStr := strings.TrimSpace(string(resp.Body))
		if !strings.EqualFold(bodyStr, description) {
			r.Content = openapi.Content{}
			r.Content.Set(openapi.MediaRange(mediaType), &openapi.MediaType{
				Schema: &openapi.Schema{Type: openapi.TypeString},
			})
		}

	case mediaType == "text/html":
		r.Content = openapi.Content{}
		r.Content.Set(openapi.MediaRange(mediaType), &openapi.MediaType{})
	}

	return r, nil
}

func isJSONMediaType(mediaType string) bool {
	return mediaType == "application/json" ||
		strings.HasSuffix(mediaType, "+json") ||
		strings.Contains(mediaType, "/json")
}
