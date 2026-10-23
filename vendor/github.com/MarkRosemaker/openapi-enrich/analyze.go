package enrich

import (
	"encoding/base64"
	"encoding/json/jsontext"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
	merge "github.com/MarkRosemaker/openapi-merge"
)

const (
	schemeNameBearer = "bearerAuth"
	schemeNameBasic  = "BasicAuth"
)

func analyzeInteraction(doc *openapi.Document, ia *cassette.Interaction) error {
	reqURL := new(ia.Request.URL)

	// 1. Initialize doc.Servers[0] from the first request if empty.
	// Use scheme+host only; path prefixes (e.g. versioning) are the caller's responsibility.
	if len(doc.Servers) == 0 {
		doc.Servers = openapi.Servers{{URL: reqURL.Scheme + "://" + reqURL.Host}}
	}

	// 2. Determine the effective server base URL for this request.
	// When the host matches the document-level server the request is at home.
	// When it differs, the path item will carry its own servers entry so clients
	// know which host to reach that path on.
	defaultURL, err := url.Parse(doc.Servers[0].URL)
	if err != nil {
		return fmt.Errorf("parsing server URL: %w", err)
	}

	reqServerURL := doc.Servers[0].URL
	altHost := reqURL.Host != defaultURL.Host
	if altHost {
		reqServerURL = reqURL.Scheme + "://" + reqURL.Host
	}

	// 3. Find or create the path item.
	matchedPath, pi := findPathItem(doc, reqURL)
	if pi == nil {
		// Detect ID-like segments and replace with {param} placeholders.
		paramPath, paramNames := newParametricPath(string(matchedPath))
		matchedPath = paramPath

		// Delete "/" placeholder if it exists and we're adding a real path.
		if matchedPath != "/" {
			delete(doc.Paths, "/")
		}

		pi = &openapi.PathItem{}

		if doc.Paths == nil {
			doc.Paths = openapi.Paths{}
		}

		doc.Paths.Set(matchedPath, pi)

		// Add path parameter definitions for each detected param.
		// Use the original (concrete) relative path for schema inference.
		if len(paramNames) > 0 {
			origPath := relativePath(reqURL, reqServerURL)
			addPathParams(pi, matchedPath, pathSegments(origPath), paramNames)
		}
	}

	// Stamp a path-level servers entry when the host differs from the document
	// server, so clients know where to send requests for this specific path.
	if altHost {
		altSrv := openapi.Server{URL: reqServerURL}
		found := false
		for _, s := range pi.Servers {
			if s.URL == altSrv.URL {
				found = true
				break
			}
		}

		if !found {
			pi.Servers = append(pi.Servers, altSrv)
		}
	}

	// 4. Find or create the operation for this HTTP method.
	op, created := getOrCreateOperation(pi, ia.Request.Method)

	// 5. Process query parameters.
	if err := processQueryParams(doc, pi, op, reqURL); err != nil {
		return fmt.Errorf("query params: %w", err)
	}

	// 6. Process request headers.
	var contentType string
	if err := processRequestHeaders(doc, pi.Parameters, op, created, ia.Request.Headers, &contentType); err != nil {
		return fmt.Errorf("request headers: %w", err)
	}

	// 7. Process request body.
	switch {
	case len(ia.Request.Body) > 0 && isJSONMediaType(contentType):
		if err := processRequestBody(op, ia.Request.Body, contentType); err != nil {
			return fmt.Errorf("request body: %w", err)
		}
	case contentType != "" && !cassette.IsText(contentType) && (ia.Request.BodyOmitted || len(ia.Request.Body) > 0):
		addBinaryRequestBody(op, contentType)
	}

	// 8. Infer operation ID if not already set.
	if op.OperationID == "" {
		op.OperationID = inferOperationID(ia.Request.Method, matchedPath)
	}

	// 9. Process response.
	if err := processResponse(op, &ia.Response); err != nil {
		return &errpath.ErrField{Field: "response", Err: err}
	}

	return nil
}

// getOrCreateOperation returns the existing operation for the given method
// or creates a new one and attaches it.
func getOrCreateOperation(pi *openapi.PathItem, method string) (op *openapi.Operation, created bool) {
	for m, op := range pi.Operations {
		if strings.EqualFold(m, method) {
			return op, false
		}
	}

	op = &openapi.Operation{}
	pi.SetOperation(method, op)

	return op, true
}

func processQueryParams(doc *openapi.Document, pi *openapi.PathItem, op *openapi.Operation, reqURL *url.URL) error {
	query := reqURL.Query()
	for _, name := range slices.Sorted(maps.Keys(query)) {
		values := query[name]
		value := strings.Join(values, ",")

		var (
			schema       *openapi.Schema
			explodeFalse bool
		)

		if strings.Contains(value, ",") {
			// Comma-separated → non-exploded array
			items, err := commaListSchema(value)
			if err != nil {
				return fmt.Errorf("param %q: %w", name, err)
			}

			schema = &openapi.Schema{
				Type:  openapi.TypeArray,
				Items: items,
			}
			explodeFalse = true
		} else {
			switch value {
			case "true", "false":
				schema = &openapi.Schema{
					Type:    openapi.TypeBoolean,
					Example: jsontext.Value(value),
				}
			default:
				var err error

				schema, err = scalarSchema(value)
				if err != nil {
					return fmt.Errorf("param %q: %w", name, err)
				}
			}
		}

		incoming := &openapi.Parameter{
			Name:   name,
			In:     openapi.ParameterLocationQuery,
			Schema: schema,
		}
		if explodeFalse {
			incoming.Explode = new(false)
		}

		if err := addParam(doc, pi.Parameters, op, incoming); err != nil {
			return fmt.Errorf("merging param %q: %w", name, err)
		}
	}

	return nil
}

// processRequestHeaders reads the request headers h into op; created says op has only just been added, so the
// document's security says nothing yet about what it requires.
func processRequestHeaders(doc *openapi.Document, piParams openapi.ParameterList, op *openapi.Operation, created bool, h http.Header, contentType *string) error {
	if h.Get("Authorization") == "" {
		allowAnonymous(doc, op, created)
	}

	for _, key := range slices.Sorted(maps.Keys(h)) {
		// assume all keys are stored in canonical form
		if canonical := http.CanonicalHeaderKey(key); canonical != key {
			return fmt.Errorf("header %q: not in canonical form (%q)", key, canonical)
		}

		val := h.Get(key)

		switch key {
		case "Authorization":
			if err := processAuth(doc, op, created, val); err != nil {
				return fmt.Errorf("%s: %w", val, err)
			}
		case "Content-Type":
			mt, _, err := mime.ParseMediaType(val)
			if err != nil {
				return fmt.Errorf("parsing media type %q: %w", val, err)
			}

			*contentType = mt
		case "User-Agent", "Referer", "Cookie":
			// ignored
		default:
			if isCustomHeader(key) {
				if err := processCustomHeader(doc, piParams, op, key, val); err != nil {
					return fmt.Errorf("header %q: %w", key, err)
				}
			}

			// Standard headers we don't model are silently skipped.
		}
	}

	return nil
}

func processAuth(doc *openapi.Document, op *openapi.Operation, created bool, v string) error {
	var scheme, schemeName string

	switch {
	case strings.HasPrefix(v, "Bearer "):
		scheme = openapi.SecuritySchemeBearer
		schemeName = schemeNameBearer
	case strings.HasPrefix(v, "Basic "):
		scheme = openapi.SecuritySchemeBasic
		schemeName = schemeNameBasic

		// Validate it is either masked or actually base64
		encoded := strings.TrimPrefix(v, "Basic ")
		if encoded != strings.Repeat("*", len(encoded)) {
			if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
				return fmt.Errorf("invalid basic auth: %w", err)
			}
		}
	default:
		return nil
	}

	// Add security scheme to components if not present.
	if doc.Components.SecuritySchemes == nil {
		doc.Components.SecuritySchemes = openapi.SecuritySchemes{}
	}

	name := openapi.SecuritySchemeName(schemeName)
	if _, ok := doc.Components.SecuritySchemes[name]; !ok {
		doc.Components.SecuritySchemes.Set(name, &openapi.SecuritySchemeRef{
			Value: &openapi.SecurityScheme{
				Type:   openapi.SecuritySchemeTypeHTTP,
				Scheme: scheme,
			},
		})
	}

	requireAuth(doc, op, created, openapi.SecurityRequirement{name: []string{}})

	return nil
}

func processCustomHeader(doc *openapi.Document, piParams openapi.ParameterList, op *openapi.Operation, name, value string) error {
	schema, err := scalarSchema(value)
	if err != nil {
		return err
	}

	return addParam(doc, piParams, op, &openapi.Parameter{
		Name:     name,
		In:       openapi.ParameterLocationHeader,
		Required: true,
		Schema:   schema,
	})
}

func processRequestBody(op *openapi.Operation, body []byte, contentType string) error {
	schema, err := newSchemaFromJSON(body)
	if err != nil {
		return err
	}

	mr := openapi.MediaRange(contentType)
	if op.RequestBody == nil {
		op.RequestBody = &openapi.RequestBodyRef{Value: &openapi.RequestBody{
			Required: true,
			Content:  openapi.Content{},
		}}
		op.RequestBody.Value.Content.Set(mr, &openapi.MediaType{
			Schema: schema,
		})

		return nil
	}

	rb := op.RequestBody.Value
	if existing, ok := rb.Content[mr]; ok {
		if existing.Schema != nil {
			if err := merge.Schema(deref(existing.Schema), schema, false); err != nil {
				return err
			}

			return growEnums(deref(existing.Schema), body)
		}

		existing.Schema = schema

		return nil
	}

	rb.Content.Set(mr, &openapi.MediaType{Schema: schema})

	return nil
}

func processResponse(op *openapi.Operation, resp *cassette.Response) error {
	incoming, err := buildResponse(resp)
	if err != nil {
		return fmt.Errorf("building response: %w", err)
	}

	sc := openapi.StatusCode(strconv.Itoa(resp.StatusCode))

	if op.Responses == nil {
		op.Responses = openapi.OperationResponses{} //nolint:exhaustive
	}

	existing, ok := op.Responses[sc]
	if !ok {
		op.Responses.Set(sc, &openapi.ResponseRef{Value: incoming})
		return nil
	}

	if err := merge.Response(existing.Value, incoming); err != nil {
		return err
	}

	for _, mt := range existing.Value.Content {
		if mt.Schema == nil {
			continue
		}

		if err := growEnums(deref(mt.Schema), resp.Body); err != nil {
			return &errpath.ErrField{Field: "content", Err: err}
		}
	}

	return nil
}

// componentParamRef returns a $ref to the component parameter with the given
// name and location, or nil if the document declares none. Components are
// keyed by an arbitrary name (e.g. "NotionVersionHeader"), not by the
// parameter's own name, so they have to be searched by value.
func componentParamRef(doc *openapi.Document, name string, in openapi.ParameterLocation) *openapi.ParameterRef {
	for key, p := range doc.Components.Parameters.ByIndex() {
		if p.Value != nil && p.Value.Name == name && p.Value.In == in {
			return &openapi.ParameterRef{
				Value: p.Value,
				Ref:   &openapi.Reference{Identifier: "#/components/parameters/" + key},
			}
		}
	}

	return nil
}

// addParam merges incoming into the matching parameter of the path item or
// operation, or else into a matching component parameter, which the
// operation then references; only when neither exists is incoming added
// inline.
func addParam(doc *openapi.Document, piParams openapi.ParameterList, op *openapi.Operation, incoming *openapi.Parameter) error {
	if existing := findParam(piParams, op.Parameters, incoming.Name, incoming.In); existing != nil {
		return merge.Parameter(existing, incoming)
	}

	if ref := componentParamRef(doc, incoming.Name, incoming.In); ref != nil {
		if ref.Value.Schema != nil && deref(ref.Value.Schema).Type == "" {
			deref(ref.Value.Schema).Type = openapi.TypeString
		}

		op.Parameters = append(op.Parameters, ref)

		return merge.Parameter(ref.Value, incoming)
	}

	op.Parameters = append(op.Parameters, &openapi.ParameterRef{Value: incoming})

	return nil
}

// findParam finds a parameter by name and location in a ParameterList.
func findParam(piParams, opParams openapi.ParameterList, name string, in openapi.ParameterLocation) *openapi.Parameter {
	for _, p := range append(opParams, piParams...) {
		if p.Value != nil && p.Value.Name == name && p.Value.In == in {
			if deref(p.Value.Schema).Type == "" {
				deref(p.Value.Schema).Type = openapi.TypeString
			}

			return p.Value
		}
	}

	return nil
}

// scalarSchema infers a schema from a single string value.
func scalarSchema(value string) (*openapi.Schema, error) {
	if _, err := strconv.Atoi(value); err == nil {
		return &openapi.Schema{Type: openapi.TypeInteger}, nil
	}

	return newSchemaFromJSON(fmt.Appendf(nil, "%q", value))
}

// commaListSchema infers the item schema from a comma-separated string.
func commaListSchema(value string) (*openapi.Schema, error) {
	parts := strings.Split(value, ",")

	var itemSchema *openapi.Schema
	for _, part := range parts {
		s, err := scalarSchema(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}

		if itemSchema == nil {
			itemSchema = s
		} else {
			if err := merge.Schema(itemSchema, s, false); err != nil {
				return nil, err
			}
		}
	}

	return itemSchema, nil
}

// standardRequestHeaders is the set of request headers that are not captured
// as parameters — they are either handled specially or not meaningful to model.
var standardRequestHeaders = map[string]struct{}{
	"Accept":              {},
	"Accept-Charset":      {},
	"Accept-Encoding":     {},
	"Accept-Language":     {},
	"Cache-Control":       {},
	"Connection":          {},
	"Host":                {},
	"If-Match":            {},
	"If-Modified-Since":   {},
	"If-None-Match":       {},
	"If-Unmodified-Since": {},
	"Origin":              {},
	"Pragma":              {},
	"Range":               {},
	"Te":                  {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

// addPathParams adds Required path Parameter definitions to pi for each
// detected parameter name. The schema is inferred from the concrete segment value.
func addPathParams(pi *openapi.PathItem, p openapi.Path, reqSegments, paramNames []string) {
	pp := parsePath(string(p))
	for i, el := range pp {
		if !el.isParam {
			continue
		}

		// Look up the concrete segment value from the original request segments.
		var schema *openapi.Schema
		if i < len(reqSegments) {
			if _, err := strconv.Atoi(reqSegments[i]); err == nil {
				schema = &openapi.Schema{Type: openapi.TypeInteger}
			} else {
				schema = &openapi.Schema{Type: openapi.TypeString}
			}
		} else {
			schema = &openapi.Schema{Type: openapi.TypeString}
		}

		pi.Parameters = append(pi.Parameters, &openapi.ParameterRef{
			Value: &openapi.Parameter{
				Name:     el.name,
				In:       openapi.ParameterLocationPath,
				Required: true,
				Schema:   schema,
			},
		})
	}
}

// isCustomHeader reports whether a canonical header name should be captured
// as a parameter — X-* vendor headers or any unknown non-standard header.
func isCustomHeader(key string) bool {
	if strings.HasPrefix(key, "X-") {
		return true
	}

	_, isStd := standardRequestHeaders[key]

	return !isStd
}

// addBinaryRequestBody gives op a request body of the media type contentType that is not text, such as a file
// upload, if it has none of that type yet: a string of bytes, as the recording does not keep it.
func addBinaryRequestBody(op *openapi.Operation, contentType string) {
	if op.RequestBody == nil {
		op.RequestBody = &openapi.RequestBodyRef{Value: &openapi.RequestBody{Required: true, Content: openapi.Content{}}}
	}

	rb := op.RequestBody.Value
	if rb == nil {
		return
	}

	if _, ok := rb.Content[openapi.MediaRange(contentType)]; ok {
		return
	}

	if rb.Content == nil {
		rb.Content = openapi.Content{}
	}

	rb.Content.Set(openapi.MediaRange(contentType), &openapi.MediaType{Schema: binarySchema()})
}

// binarySchema is the schema of a body that is not text: a string of bytes.
func binarySchema() *openapi.Schema {
	return &openapi.Schema{Type: openapi.TypeString, Format: openapi.FormatBinary}
}
