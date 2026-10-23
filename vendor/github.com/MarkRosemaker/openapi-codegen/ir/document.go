package ir

import (
	"cmp"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/MarkRosemaker/openapi"
	compress "github.com/MarkRosemaker/openapi-compress"
	flatten "github.com/MarkRosemaker/openapi-flatten"
	"github.com/MarkRosemaker/ordmap"
	"github.com/ettle/strcase"
)

// FromDocument converts a fully-loaded and flattened openapi.Document to an IR Document.
// cfg provides the package name and optional user-agent override. In debug mode, nothing the specification leaves
// open decodes into any: see narrowUnspecified.
func FromDocument(doc *openapi.Document, packageName, userAgent string, production, debug bool) (*Document, error) {
	if err := flatten.Document(doc, flatten.Config{MarkOrigin: true}); err != nil {
		return nil, fmt.Errorf("flatten: %w", err)
	}

	if err := compress.Document(doc, compress.Config{}); err != nil {
		return nil, fmt.Errorf("compress: %w", err)
	}

	if err := exportComponentNames(doc); err != nil {
		return nil, fmt.Errorf("export names: %w", err)
	}

	if debug {
		narrowUnspecified(doc)
	}

	// sort the components and responses
	// (but not the paths since it may be good to keep them in the order they were added)
	doc.Components.SortMaps()

	for _, path := range doc.Paths {
		for _, op := range path.Operations {
			op.Responses.Sort()
		}
	}

	baseURL, err := parseBaseURL(doc)
	if err != nil {
		return nil, fmt.Errorf("servers: %w", err)
	}

	if userAgent == "" && doc.Info != nil {
		userAgent = doc.Info.Title
	}

	uses, err := countSchemaUses(doc)
	if err != nil {
		return nil, err
	}

	schemas, err := fromComponentSchemas(doc.Components.Schemas, uses)
	if err != nil {
		return nil, fmt.Errorf("components.schemas: %w", err)
	}

	auth, schemes := fromSecurity(doc.Components.SecuritySchemes, doc.Info.Title)
	auth.Default = defaultAuth(doc.Security, schemes, auth)

	globalParamsMap, err := getGlobalParams(doc.Paths, doc.Info.Title)
	if err != nil {
		return nil, fmt.Errorf("getting global parameters: %w", err)
	}

	operations, err := fromPaths(doc.Paths, doc.Security, auth.Default, schemes, globalParamsMap)
	if err != nil {
		return nil, fmt.Errorf("paths: %w", err)
	}

	hasURL, hasDuration, hasDate, hasDateTimeOrInt := needsSpecialImports(schemas, operations)

	encodings, err := markEncodings(schemas)
	if err != nil {
		return nil, fmt.Errorf("components.schemas: %w", err)
	}

	globalParams := make(Params, 0, len(globalParamsMap))
	for _, p := range globalParamsMap.ByIndex() {
		globalParams = append(globalParams, p)
	}

	slices.SortFunc(globalParams, func(a, b Param) int {
		return strings.Compare(a.JSONName, b.JSONName)
	})

	title := ""
	if doc.Info != nil {
		title = strings.TrimSpace(doc.Info.Title)
	}

	if title == "" || title == "API" {
		title = fmt.Sprintf("%s API", strcase.ToCase(packageName, strcase.TitleCase, ' '))
	}

	return &Document{
		Title:                  title,
		Debug:                  debug,
		Production:             production,
		PackageName:            packageName,
		BaseURL:                baseURL,
		UserAgent:              userAgent,
		Schemas:                schemas,
		Operations:             operations,
		GlobalParams:           globalParams,
		Auth:                   auth,
		HasURLFields:           hasURL,
		HasDurationFields:      hasDuration,
		HasDateFields:          hasDate,
		HasDateTimeOrIntFields: hasDateTimeOrInt,
		HasStringOrNumberFields: slices.ContainsFunc(schemas, func(s Schema) bool {
			return slices.ContainsFunc(s.Fields, func(f Field) bool { return f.IsStringOrNumber })
		}),
		HasUnixTimeFields:   encodings["time.Time"][encodingUnix].all,
		HasUnixDateFields:   encodings["civil.Date"][encodingUnix].all,
		UnixTimeMembers:     encodings["time.Time"][encodingUnix].members,
		UnixDateMembers:     encodings["civil.Date"][encodingUnix].members,
		HasBasicDateFields:  encodings["civil.Date"][encodingBasic].all,
		BasicDateMembers:    encodings["civil.Date"][encodingBasic].members,
		HasStringDateFields: hasEncoding(encodings, "civil.Date", ""),
		// a date-time that may be an integer is read by an unmarshaler of every time.Time, which comes first
		HasStringTimeFields: hasEncoding(encodings, "time.Time", "") && !hasDateTimeOrInt,
		HasServerOverrides: slices.ContainsFunc(operations, func(op Operation) bool {
			return op.BaseURL != nil
		}),
	}, nil
}

// parseBaseURL extracts scheme, host, and path from the first server URL.
func parseBaseURL(doc *openapi.Document) (URLParts, error) {
	if len(doc.Servers) == 0 {
		return URLParts{Scheme: "https"}, nil
	}

	return parseServer(doc.Servers[0].URL)
}

func parseServer(raw string) (URLParts, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return URLParts{}, fmt.Errorf("parse %q: %w", raw, err)
	}

	return URLParts{
		Scheme: u.Scheme,
		Host:   u.Host,
		Path:   strings.TrimSuffix(u.Path, "/"),
	}, nil
}

// fromPaths iterates all path items and operations, converting each to ir.Operation.
func fromPaths(paths openapi.Paths, docSecurity openapi.SecurityRequirements, defaultAuth AuthScheme, schemes map[openapi.SecuritySchemeName]AuthScheme, globalParams paramMap) ([]Operation, error) {
	var ops []Operation
	for path, item := range paths.ByIndex() {
		// A path item may name its own server: SEC serves one path from
		// www.sec.gov and the rest from data.sec.gov. Without this the client
		// sends every request to the document's server.
		var override *URLParts
		if len(item.Servers) > 0 {
			parts, err := parseServer(item.Servers[0].URL)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}

			override = &parts
		}

		for method, op := range item.Operations {
			irOp, err := FromOperation(path, item.Parameters, method, op, globalParams)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}

			irOp.BaseURL = override
			irOp.Auth = defaultAuth
			irOp.AuthOptional = optionalAuth(docSecurity)

			if op.Security != nil {
				irOp.Auth = requiredAuth(op.Security, schemes)
				irOp.AuthOptional = optionalAuth(op.Security)
			}

			ops = append(ops, *irOp)
		}
	}

	return ops, nil
}

// fromSecurity reads the security schemes. Bearer.Name is the environment
// variable the generated client reads the token from, and everything the
// client emits for bearer auth hangs off it being set. Basic's two env
// vars work the same way, one for the username and one for the password.
//
// A scheme's name field only means anything for type apiKey, where it names
// the header, so an http scheme usually leaves it empty. Falling back
// to the title matches how an API key parameter gets its own variable.
func fromSecurity(schemes openapi.SecuritySchemes, apiTitle string) (Auth, map[openapi.SecuritySchemeName]AuthScheme) {
	s := Auth{}
	byName := map[openapi.SecuritySchemeName]AuthScheme{}

	for name, sec := range schemes {
		v := sec.Value
		switch v.Scheme {
		case openapi.SecuritySchemeBearer:
			byName[name] = AuthBearer

			name := v.Name
			if name == "" && apiTitle != "" {
				name = strcase.ToSNAKE(fmt.Sprintf("%s_TOKEN", apiTitle))
			}

			s.Bearer = Bearer{Name: name}
		case openapi.SecuritySchemeBasic:
			byName[name] = AuthBasic

			if apiTitle != "" {
				s.Basic = Basic{
					UsernameEnvName: strcase.ToSNAKE(fmt.Sprintf("%s_USERNAME", apiTitle)),
					PasswordEnvName: strcase.ToSNAKE(fmt.Sprintf("%s_PASSWORD", apiTitle)),
				}
			}
		}
	}

	return s, byName
}

// defaultAuth is the scheme the document's security requires, or without any, the one scheme it defines.
func defaultAuth(security openapi.SecurityRequirements, schemes map[openapi.SecuritySchemeName]AuthScheme, auth Auth) AuthScheme {
	switch {
	case security != nil:
		return requiredAuth(security, schemes)
	case auth.Bearer.Name != "":
		return AuthBearer
	case auth.Basic.UsernameEnvName != "":
		return AuthBasic
	default:
		return ""
	}
}

// optionalAuth reports whether security lets a call go without credentials beside requiring some: it lists the empty
// requirement among others.
func optionalAuth(security openapi.SecurityRequirements) bool {
	return len(security) > 1 && slices.ContainsFunc(security, func(r openapi.SecurityRequirement) bool { return len(r) == 0 })
}

// requiredAuth is the first scheme the client supports among the alternatives a security requirement lists.
func requiredAuth(security openapi.SecurityRequirements, schemes map[openapi.SecuritySchemeName]AuthScheme) AuthScheme {
	for _, req := range security {
		for _, name := range slices.Sorted(maps.Keys(req)) {
			if a, ok := schemes[name]; ok {
				return a
			}
		}
	}

	return ""
}

type paramMap = ordmap.OrderedMap[*openapi.Parameter, Param]

func getGlobalParams(paths openapi.Paths, apiTitle string) (paramMap, error) {
	params := paramMap{}

	for _, pi := range paths {
		for _, pRef := range pi.Parameters {
			p := pRef.Value
			if isIgnoredParam(p) {
				continue
			}

			param, err := fromParam(p, apiTitle)
			if err != nil {
				return nil, err
			}

			// A description on the $ref itself overrides the referenced
			// component's own, per the Reference Object spec.
			ref := cmp.Or(pRef.Ref, &openapi.Reference{})
			param.Description = cmp.Or(ref.Description, param.Description)

			if param.GlobalType != "" {
				params.Set(p, param)
			}
		}
	}

	for p := range params {
		if !isInAll(paths, p) {
			delete(params, p)
		}
	}

	return params, nil
}

func isInAll(paths openapi.Paths, p *openapi.Parameter) bool {
	for _, pi := range paths {
		if !slices.ContainsFunc(pi.Parameters, func(param *openapi.ParameterRef) bool {
			return param.Value == p
		}) {
			return false
		}
	}

	return true
}

// needsSpecialImports scans schemas and operation types for url.URL, time.Duration, civil.Date, and any
// date-time-or-integer oneOf fields.
func needsSpecialImports(schemas []Schema, ops []Operation) (hasURL, hasDuration, hasDate, hasDateTimeOrInt bool) {
	check := func(goType string) {
		if containsType(goType, "url.URL") {
			hasURL = true
		}

		if containsType(goType, "time.Duration") {
			hasDuration = true
		}

		if containsType(goType, "civil.Date") {
			hasDate = true
		}
	}

	for _, s := range schemas {
		for _, f := range s.Fields {
			check(f.Type)

			if f.IsDateTimeOrInt {
				hasDateTimeOrInt = true
			}
		}
	}

	for _, op := range ops {
		for _, p := range op.PathParams {
			check(p.Type)
		}

		for _, p := range op.QueryParams {
			check(p.Type)
		}

		if op.SuccessReturn != nil {
			check(op.SuccessReturn.Name)
		}
	}

	return hasURL, hasDuration, hasDate, hasDateTimeOrInt
}

func containsType(goType, needle string) bool {
	return goType == needle || strings.Contains(goType, needle)
}
