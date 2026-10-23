package ir

import (
	"cmp"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi"
	"github.com/ettle/strcase"
)

// FromOperation converts an openapi operation to its IR representation.
// pathItemParams are the parameters defined at the path item level and are merged
// with (and can be overridden by) operation-level parameters.
func FromOperation(
	rawPath openapi.Path,
	pathItemParams openapi.ParameterList,
	method string,
	op *openapi.Operation,
	globalParams paramMap,
) (*Operation, error) {
	if op.OperationID == "" {
		return nil, fmt.Errorf("operationId is required")
	}

	name := strcase.ToGoPascal(op.OperationID)

	// Merge path-item parameters with operation parameters (operation overrides on conflict).
	merged := mergeParams(pathItemParams, op.Parameters)

	parsedPath := rawPath.Parse()

	// Resolve each parameter and index by name for path arg computation.
	var pathParams, queryParams, headerParams, fixedParams []Param

	paramByName := make(map[string]Param, len(merged))

	for _, pRef := range merged {
		p := pRef.Value
		if _, ok := globalParams[p]; ok {
			continue
		}

		param, err := fromParam(p, "")
		if err != nil {
			return nil, fmt.Errorf("param %q: %w", p.Name, err)
		}

		// A description on the $ref itself overrides the referenced
		// component's own, per the Reference Object spec. Applied here
		// rather than onto p.Description: p is the shared component value,
		// so writing through it would leak this operation's override into
		// every other reference to the same component.
		ref := cmp.Or(pRef.Ref, &openapi.Reference{})
		param.Description = cmp.Or(ref.Description, param.Description)

		paramByName[p.Name] = param

		if param.Value != "" {
			param.In = string(p.In)
			fixedParams = append(fixedParams, param)

			continue
		}

		switch p.In {
		case openapi.ParameterLocationPath:
			pathParams = append(pathParams, param)
		case openapi.ParameterLocationQuery:
			queryParams = append(queryParams, param)
		case openapi.ParameterLocationHeader:
			headerParams = append(headerParams, param)
		default:
		}
	}

	joinArgs := buildJoinPathArgs(parsedPath, paramByName)

	hasParams := len(pathParams)+len(queryParams)+len(headerParams) > 0

	var paramStructName string
	if len(queryParams)+len(headerParams) > 0 {
		paramStructName = name + "Params"
	}

	var reqBody *ReqBody
	if op.RequestBody != nil && op.RequestBody.Value != nil {
		var err error

		reqBody, err = fromRequestBody(op.RequestBody.Value)
		if err != nil {
			return nil, fmt.Errorf("requestBody: %w", err)
		}
	}

	responses, successReturn, rawBytesSuccess, streamSuccess, emptySuccess, err := fromResponses(op.Responses)
	if err != nil {
		return nil, fmt.Errorf("responses: %w", err)
	}

	return &Operation{
		Name:            name,
		Description:     op.Description,
		Summary:         op.Summary,
		Method:          strings.ToUpper(method),
		PathTemplate:    string(rawPath),
		JoinPathArgs:    joinArgs,
		PathParams:      pathParams,
		QueryParams:     queryParams,
		HeaderParams:    headerParams,
		FixedParams:     fixedParams,
		HasParams:       hasParams,
		ParamStructName: paramStructName,
		RequestBody:     reqBody,
		Responses:       responses,
		SuccessReturn:   successReturn,
		Deprecated:      op.Deprecated,
		RawBytesSuccess: rawBytesSuccess,
		StreamSuccess:   streamSuccess,
		EmptySuccess:    emptySuccess,
	}, nil
}

// mergeParams merges path-item params with operation params; operation wins on (name, in) collision.
func mergeParams(pathItem, operation openapi.ParameterList) openapi.ParameterList {
	if len(pathItem) == 0 {
		return operation
	}

	overrides := make(map[string]bool, len(operation))
	for _, pRef := range operation {
		overrides[string(pRef.Value.In)+":"+pRef.Value.Name] = true
	}

	result := make(openapi.ParameterList, 0, len(pathItem)+len(operation))
	for _, pRef := range pathItem {
		if !overrides[string(pRef.Value.In)+":"+pRef.Value.Name] {
			result = append(result, pRef)
		}
	}

	return append(result, operation...)
}

// paramSchema is the schema a parameter is sent as. Null is dropped, a union of an array and its items is the array:
// form style writes one value alike. Any other union is a string, the one type that can carry each of its values.
func paramSchema(s *openapi.Schema) *openapi.Schema {
	d := deref(s)
	variants := d.OneOf
	if len(variants) == 0 {
		variants = d.AnyOf
	}

	if d.Type != "" || len(variants) == 0 {
		return s
	}

	// a query string cannot carry null, so only the other alternatives matter
	variants = slices.DeleteFunc(slices.Clone(variants), isNull)
	if len(variants) == 1 {
		return paramSchema(variants[0])
	}

	str := &openapi.Schema{Type: openapi.TypeString}

	var array *openapi.Schema
	for _, v := range variants {
		switch deref(v).Type {
		case openapi.TypeString:
		case openapi.TypeArray:
			if array != nil {
				return str
			}

			array = v
		default:
			return str
		}
	}

	if array != nil {
		return array
	}

	return str
}

// setType sets the parameter's Go type, and how to format and parse it, from s.
func (p *Param) setType(s *openapi.Schema) error {
	p.IsEnum = len(deref(s).Enum) > 0

	// SchemaGoType(s), not of deref(s): a $ref resolves to
	// its own generated type name (e.g. a string enum), where reading the
	// resolved value directly would only ever see its underlying builtin.
	tp, err := SchemaGoType(s)
	if err != nil {
		return err
	}

	p.Type = tp.String()

	if s.Ref != nil {
		// Type is a generated name here (an enum or any other named
		// scalar component), not a builtin: NotZero and FormatExpr need
		// the underlying representation it was declared from to know how
		// to compare or format it.
		base, err := SchemaGoType(s.Ref.Value)
		if err != nil {
			return err
		}

		p.BaseType = base.String()
	}

	p.IsUnix = isUnix(p.Type, deref(s))
	p.IsDateBasic = isDateBasic(p.Type, deref(s))

	if p.BaseType == "" {
		p.ParseExpr, p.ParseConv, p.ParseErrFree = parseExpr(p.Type, p.IsUnix)
		if p.IsDateBasic {
			p.ParseExpr, p.ParseConv, p.ParseErrFree = `time.Parse("20060102", %s)`, "civil.DateOf(%s)", false
		}

		return nil
	}

	// a generated type is parsed as the type it was declared from, then converted
	p.ParseExpr, p.ParseConv, p.ParseErrFree = parseExpr(p.BaseType, deref(s).Type == openapi.TypeInteger)
	if p.ParseErrFree {
		p.ParseExpr = p.Type + "(" + p.ParseExpr + ")"
	} else {
		p.ParseConv = p.Type + "(" + cmp.Or(p.ParseConv, "%s") + ")"
	}

	return nil
}

func fromParam(p *openapi.Parameter, apiTitle string) (Param, error) {
	param := Param{
		JSONName:    p.Name,
		Required:    p.Required,
		Description: p.Description,
	}
	if p.Schema == nil {
		return param, fmt.Errorf("schema is required")
	}

	param.GoName = strcase.ToGoCamel(p.Name)

	param.FieldName = strcase.ToGoPascal(p.Name)

	schema := paramSchema(p.Schema)
	switch d := deref(schema); {
	case d.Type == openapi.TypeArray && d.Items != nil:
		item := &Param{VarName: "v"}
		if err := item.setType(paramSchema(d.Items)); err != nil {
			return param, fmt.Errorf("items: %w", err)
		}

		param.Item = item
		param.Type = "[]" + item.Type

		if schema.Ref != nil { // a named array, such as Modules, keeps its name
			tp, err := SchemaGoType(schema)
			if err != nil {
				return param, err
			}

			param.Type = tp.String()
		}
	case d.Type == openapi.TypeObject && p.In == openapi.ParameterLocationQuery:
		if err := param.setObject(schema); err != nil {
			return param, err
		}
	default:
		if err := param.setType(schema); err != nil {
			return param, err
		}
	}

	if p.In == openapi.ParameterLocationQuery {
		param.setStyle(p)
	}

	if p.Required {
		switch p.Name {
		// X-Rd-Token is an API token, so it gets the same treatment: a client
		// field, a ClientOption, and a value read from the environment. It
		// shares VarName with X-Api-Key because only one of the two can be
		// rendered -- Document.APIKey returns a single parameter.
		case "X-Api-Key", "X-Rd-Token":
			param.GlobalType = GlobalAPIKey
			param.VarName = "apiKey"
			if apiTitle != "" {
				param.EnvName = strcase.ToSNAKE(fmt.Sprintf("%s_KEY", apiTitle))
			}
		case "X-Client":
			param.GlobalType = GlobalClient
			param.VarName = "client"
		default:
			param.Value = fixedValue(deref(schema))
		}
	}

	if param.VarName == "" {
		if p.In == openapi.ParameterLocationPath {
			param.VarName = strcase.ToGoCamel(p.Name)
		} else {
			param.VarName = "params." + param.FieldName
		}
	}

	for i, prop := range param.Props {
		param.Props[i].VarName = param.VarName + "." + prop.FieldName
	}

	// A nil example renders as the literal "null", which would reach the
	// templates as a bare identifier rather than a Go string.
	if param.GlobalType != "" && deref(p.Schema).Example != nil {
		param.Example = deref(p.Schema).Example.String()
	}

	return param, nil
}

// parseExpr is how a server parses a string into a value of the Go type named, as Param.ParseExpr, Param.ParseConv
// and Param.ParseErrFree describe, reading what FormatExpr writes: an integer date-time as a Unix time and a duration
// in whole seconds.
func parseExpr(goType string, unixTime bool) (expr, conv string, errFree bool) {
	switch goType {
	case "string":
		return "%s", "", true
	case "types.Email":
		return "types.Email(%s)", "", true
	case "bool":
		return "strconv.ParseBool(%s)", "", false
	case "int":
		return "strconv.Atoi(%s)", "", false
	case "int32":
		return "strconv.ParseInt(%s, 10, 32)", "int32(%s)", false
	case "int64":
		return "strconv.ParseInt(%s, 10, 64)", "", false
	case "uint":
		return "strconv.ParseUint(%s, 10, 64)", "uint(%s)", false
	case "uint32":
		return "strconv.ParseUint(%s, 10, 32)", "uint32(%s)", false
	case "uint64":
		return "strconv.ParseUint(%s, 10, 64)", "", false
	case "float32":
		return "strconv.ParseFloat(%s, 32)", "float32(%s)", false
	case "float64":
		return "strconv.ParseFloat(%s, 64)", "", false
	case "uuid.UUID":
		return "uuid.Parse(%s)", "", false
	case "url.URL":
		return "url.Parse(%s)", "*%s", false
	case "time.Time":
		if unixTime {
			return "strconv.ParseInt(%s, 10, 64)", "time.Unix(%s, 0)", false
		}

		return "time.Parse(time.RFC3339, %s)", "", false
	case "civil.Date":
		if unixTime {
			return "strconv.ParseInt(%s, 10, 64)", "civil.DateOf(time.Unix(%s, 0).UTC())", false
		}

		return "civil.ParseDate(%s)", "", false
	case "net.IP":
		return "net.ParseIP(%s)", "", true
	case "time.Duration":
		return "strconv.ParseInt(%s, 10, 64)", "time.Duration(%s) * time.Second", false
	default:
		// string-based enum or other cast from string
		return goType + "(%s)", "", true
	}
}

// buildJoinPathArgs produces the ordered list of Go expressions for url.JoinPath.
// e.g. "/apis/{id}/items" → [`"apis"`, `strconv.Itoa(id)`, `"items"`]
func buildJoinPathArgs(parsed openapi.ParsedPath, params map[string]Param) []string {
	segments := strings.Split(strings.TrimPrefix(parsed.String(), "/"), "/")
	args := make([]string, 0, len(segments))
	for _, seg := range segments {
		if seg == "" {
			continue
		}

		args = append(args, segmentExpr(seg, params))
	}

	return args
}

// segmentExpr returns the Go expression for one path segment, substituting
// every {param} placeholder in it.
//
// A segment is usually a placeholder and nothing else, but it can also carry
// one: sec's /api/xbrl/companyfacts/CIK{cik}.json wraps the parameter in a
// prefix and a suffix. Such a segment used to be quoted whole, leaving the
// braces in the request path.
func segmentExpr(seg string, params map[string]Param) string {
	var parts []string

	for {
		open := strings.IndexByte(seg, '{')
		if open < 0 {
			break
		}

		close := strings.IndexByte(seg[open:], '}')
		if close < 0 {
			break
		}

		close += open

		if open > 0 {
			parts = append(parts, strconv.Quote(seg[:open]))
		}

		name := seg[open+1 : close]
		if p, ok := params[name]; ok {
			parts = append(parts, p.FormatExpr())
		} else {
			parts = append(parts, strconv.Quote(name))
		}

		seg = seg[close+1:]
	}

	if seg != "" || len(parts) == 0 {
		parts = append(parts, strconv.Quote(seg))
	}

	return strings.Join(parts, " + ")
}

// NotZero returns the Go boolean expression that is true when param is not the zero value.
func (p Param) NotZero() string {
	switch {
	case p.Pointer:
		return p.VarName + " != nil"
	case p.Item != nil, p.MapValue != nil:
		return "len(" + p.VarName + ") > 0"
	}

	// A generated Type (BaseType set) carries no zero-value semantics of
	// its own: switch on the type it was declared from instead.
	// Comparisons against an untyped constant ("", 0) work directly on
	// the named type, no conversion needed.
	tp := p.Type
	if p.BaseType != "" {
		tp = p.BaseType
	}

	switch tp {
	case "string":
		return p.VarName + ` != ""`
	case "types.Email":
		return p.VarName + ` != ""`
	case "bool":
		return p.VarName
	case "uuid.UUID":
		return p.VarName + " != uuid.Nil()"
	case "net.IP":
		return p.VarName + " != nil"
	case "url.URL":
		return p.VarName + `.Host != ""`
	case "time.Time":
		return "!" + p.VarName + ".IsZero()"
	case "civil.Date":
		return p.VarName + " != (civil.Date{})"
	case "time.Duration":
		return p.VarName + " != 0"
	default:
		// int, int32, int64, uint*, float32, float64
		return p.VarName + " != 0"
	}
}

// formatExpr returns the Go expression that converts the param to a string for URL encoding.
func (p Param) FormatExpr() string {
	if p.Value != "" {
		return p.Value
	}

	if p.GlobalType != "" {
		return "c." + p.VarName
	}

	// A generated Type (BaseType set) has none of the methods or
	// conversions below: convert to the type it was declared from first.
	// That conversion is always legal, since it targets the generated
	// type's own underlying type.
	tp, v := p.Type, p.VarName
	if p.Pointer {
		v = "*" + v
	}

	if p.BaseType != "" {
		tp = p.BaseType
		v = tp + "(" + v + ")"
	}

	switch tp {
	case "string":
		return v
	case "types.Email":
		return "string(" + v + ")"
	case "bool":
		return "strconv.FormatBool(" + v + ")"
	case "int":
		return "strconv.Itoa(" + v + ")"
	case "int32":
		return "strconv.FormatInt(int64(" + v + "), 10)"
	case "int64":
		return "strconv.FormatInt(" + v + ", 10)"
	case "uint":
		return "strconv.FormatUint(uint64(" + v + "), 10)"
	case "uint32":
		return "strconv.FormatUint(uint64(" + v + "), 10)"
	case "uint64":
		return "strconv.FormatUint(" + v + ", 10)"
	case "float32":
		return "strconv.FormatFloat(float64(" + v + "), 'f', -1, 32)"
	case "float64":
		return "strconv.FormatFloat(" + v + ", 'f', -1, 64)"
	case "uuid.UUID":
		return v + ".String()"
	case "url.URL":
		return v + ".String()"
	case "time.Time":
		if p.IsUnix {
			return "strconv.Itoa(int(" + v + ".Unix()))"
		}

		return v + ".Format(time.RFC3339)"
	case "civil.Date":
		if p.IsUnix {
			return "strconv.FormatInt(" + v + ".In(time.UTC).Unix(), 10)"
		}

		if p.IsDateBasic {
			return v + `.In(time.UTC).Format("20060102")`
		}

		return v + ".String()"
	case "net.IP":
		return v + ".String()"
	case "time.Duration":
		return "strconv.FormatInt(int64(" + v + "/time.Second), 10)"
	default:
		return "fmt.Sprint(" + v + ")"
	}
}

func fromRequestBody(rb *openapi.RequestBody) (*ReqBody, error) {
	for mr, mt := range rb.Content.ByIndex() {
		if mt.Schema == nil {
			continue
		}

		tp, err := SchemaGoType(mt.Schema)
		if err != nil {
			return nil, err
		}

		return &ReqBody{
			TypeName:    tp.String(),
			ContentType: string(mr),
			Required:    rb.Required,
		}, nil
	}

	return nil, nil
}

func fromResponses(responses openapi.OperationResponses) (Responses, *GoType, bool, bool, bool, error) {
	var (
		result          Responses
		successReturn   *GoType
		rawBytesSuccess bool
		streamSuccess   bool
		emptySuccess    bool
	)

	for code, rRef := range responses.ByIndex() {
		r := rRef.Value

		isSuccess := code.IsSuccess()
		goConst := statusCodeToConst(code)

		// Prefer a JSON media type; if the response declares content but none
		// of it is JSON (e.g. text/plain), fall back to the first declared
		// media type and treat the body as an opaque byte stream.
		var (
			jsonContentType  string
			jsonSchema       *openapi.Schema
			firstContentType string
		)
		for mr, mt := range r.Content.ByIndex() {
			if firstContentType == "" {
				firstContentType = string(mr)
			}

			if strings.Contains(string(mr), "json") {
				jsonContentType = string(mr)
				jsonSchema = mt.Schema
				break
			}
		}

		var (
			goType      *GoType
			contentType string
			isRawBytes  bool
			isStream    bool
		)

		switch {
		case jsonContentType != "":
			contentType = jsonContentType

			if jsonSchema != nil {
				var err error

				goType, err = SchemaGoType(jsonSchema)
				if err != nil {
					return nil, nil, false, false, false, fmt.Errorf("response %s: %w", code, err)
				}
			}
		case firstContentType != "" && isSuccess && !cassette.IsText(firstContentType):
			// a zip, a PDF, an image or the like: the body itself, read as it arrives rather than all at once
			contentType = firstContentType
			goType = &GoType{Name: "io.ReadCloser", IsNilable: true}
			isStream = true
		case firstContentType != "":
			contentType = firstContentType
			goType = &GoType{Name: "byte", IsSlice: true}
			isRawBytes = true
		}

		// A description on the $ref itself overrides the referenced
		// response's own, per the Reference Object spec.
		respRef := cmp.Or(rRef.Ref, &openapi.Reference{})

		result = append(result, Response{
			StatusCode:  string(code),
			GoConstant:  goConst,
			Description: cmp.Or(respRef.Description, r.Description),
			ContentType: contentType,
			GoType:      goType,
			IsSuccess:   isSuccess,
			IsRawBytes:  isRawBytes,
			IsStream:    isStream,
		})

		// an empty object is still decoded, so anything in it is an error, but there is nothing to return
		if isSuccess && goType != nil && successReturn == nil && !emptySuccess {
			if isEmptyObject(jsonSchema) {
				emptySuccess = true
			} else {
				successReturn = goType
				rawBytesSuccess = isRawBytes
				streamSuccess = isStream
			}
		}
	}

	return result, successReturn, rawBytesSuccess, streamSuccess, emptySuccess, nil
}

// statusCodeToConst converts an OpenAPI status code to its net/http constant name.
func statusCodeToConst(code openapi.StatusCode) string {
	if code == openapi.StatusCodeDefault {
		return "default"
	}

	n, err := strconv.Atoi(string(code))
	if err != nil {
		return string(code)
	}

	text := http.StatusText(n)
	if text == "" {
		return string(code)
	}
	// "No Content" → "NoContent" → "http.StatusNoContent"
	return "http.Status" + strings.ReplaceAll(text, " ", "")
}

// fixedValue is the Go string literal of the one value s allows: its const, else the only value of its enum. It is
// empty if s allows more than one.
func fixedValue(s *openapi.Schema) string {
	var v jsontext.Value

	switch {
	case len(s.Const) > 0:
		v = s.Const
	case len(s.Enum) == 1:
		v = s.Enum[0]
	default:
		return ""
	}

	var str string
	if err := json.Unmarshal(v, &str); err == nil {
		return strconv.Quote(str)
	}

	return strconv.Quote(string(v)) // a number or a boolean is sent as it is written
}

// isEmptyObject reports whether s is an object that holds no member at all.
func isEmptyObject(s *openapi.Schema) bool {
	s = deref(s)

	return s != nil && s.Type == openapi.TypeObject && len(s.Properties) == 0 && mapValues(s) == nil &&
		len(s.AllOf) == 0 && len(s.OneOf) == 0 && len(s.AnyOf) == 0
}
