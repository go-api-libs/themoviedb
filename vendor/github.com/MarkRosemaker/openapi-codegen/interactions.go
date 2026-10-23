package codegen

import (
	"cmp"
	"encoding/json/v2"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi-codegen/ir"
)

// matchInteractions populates doc.InteractionCalls by matching each interaction
// to a known operation and extracting Go literal argument values.
// Every interaction must match exactly one operation; an error is returned otherwise,
// guaranteeing len(doc.InteractionCalls) == len(interactions) on success.
func matchInteractions(doc *ir.Document, interactions cassette.Interactions) error {
	fillGlobalParamExamples(doc, interactions)

	for _, ia := range interactions {
		if ia.Request.IsScaffold() {
			continue
		}

		call, err := interactionCall(doc, ia)
		if err != nil {
			return err
		}

		doc.InteractionCalls = append(doc.InteractionCalls, call)
	}

	return nil
}

// interactionCall is the call of the operation ia was made to, with the arguments it was made with.
func interactionCall(doc *ir.Document, ia *cassette.Interaction) (ir.InteractionCall, error) {
	u := &ia.Request.URL

	op, pathVals := pickOperation(doc.Operations, ia.Request.Method, relativePath(u.Path, doc.BaseURL.Path))
	if op == nil {
		return ir.InteractionCall{}, fmt.Errorf("interaction %s %s: no matching operation found", ia.Request.Method, u)
	}

	call := ir.InteractionCall{
		Op:         op,
		QueryArgs:  queryArgs(op, u),
		HeaderArgs: headerArgs(op.HeaderParams, ia.Request.Headers),
	}

	for _, pp := range op.PathParams {
		call.PathArgs = append(call.PathArgs, goLiteralForType(pp, pathVals[pp.JSONName]))
	}

	if op.RequestBody != nil {
		goType := op.RequestBody.TypeName
		if !op.RequestBody.Required {
			goType = "*" + goType
		}

		var raw any
		if len(ia.Request.Body) > 0 {
			if err := json.Unmarshal(ia.Request.Body, &raw); err != nil {
				return ir.InteractionCall{}, fmt.Errorf("decoding request body for %s: %w", op.Name, err)
			}
		}

		call.BodyLiteral = bodyLiteral(doc, goType, raw)
	}

	setOutcome(&call, op, ia.Response.StatusCode)

	return call, nil
}

// relativePath is path without the base URL's path, which is where the operations' paths start.
func relativePath(path, base string) string {
	rel := strings.TrimPrefix(path, base)
	if !strings.HasPrefix(rel, "/") {
		rel = "/" + rel
	}

	return rel
}

// queryArgs are the literals of the query parameters of op the query of u holds a value for.
func queryArgs(op *ir.Operation, u *url.URL) []ir.InteractionParam {
	q := u.Query()

	var args []ir.InteractionParam

	for _, qp := range op.QueryParams {
		if lit := queryLiteral(op, qp, u.RawQuery, q); lit != "" {
			args = append(args, ir.InteractionParam{FieldName: qp.FieldName, Literal: lit})
		}
	}

	return args
}

// queryLiteral is the literal of the query parameter p as written into the query string raw, decoded as q, or empty
// if it is not there.
func queryLiteral(op *ir.Operation, p ir.Param, raw string, q url.Values) string {
	switch {
	case p.MapValue != nil:
		return mapLiteral(p, mapEntries(op, p, raw, q))
	case p.Props != nil:
		return objectLiteral(p, objectEntries(p, raw, q))
	case p.Item != nil:
		values := q[p.JSONName]
		if p.Delimiter != "" {
			values = queryParts(raw, p.JSONName, p.Delimiter)
		}

		if len(values) == 0 {
			return ""
		}

		return sliceLiteral(p, values)
	case q.Get(p.JSONName) != "":
		return goLiteralForType(p, q.Get(p.JSONName))
	default:
		return ""
	}
}

// objectEntries are the values of the members of the object parameter p, by name, as written into raw, decoded as q.
func objectEntries(p ir.Param, raw string, q url.Values) map[string]string {
	if p.Delimiter != "" {
		return pairs(queryParts(raw, p.JSONName, p.Delimiter))
	}

	entries := map[string]string{}

	for _, m := range p.Props {
		key := m.JSONName
		if p.DeepObject {
			key = p.JSONName + "[" + key + "]"
		}

		if v := q.Get(key); v != "" {
			entries[m.JSONName] = v
		}
	}

	return entries
}

// mapEntries are the entries of the map parameter p of op, as written into raw, decoded as q.
func mapEntries(op *ir.Operation, p ir.Param, raw string, q url.Values) map[string]string {
	if p.Delimiter != "" {
		return pairs(queryParts(raw, p.JSONName, p.Delimiter))
	}

	names, prefixes := op.OtherQueryNames(p)
	entries := map[string]string{}

	for k, vs := range q {
		switch {
		case p.DeepObject:
			name, ok := strings.CutPrefix(k, p.JSONName+"[")
			if name, found := strings.CutSuffix(name, "]"); ok && found {
				entries[name] = vs[0]
			}
		case !slices.Contains(names, k) &&
			!slices.ContainsFunc(prefixes, func(prefix string) bool { return strings.HasPrefix(k, prefix) }):
			entries[k] = vs[0]
		}
	}

	return entries
}

// pairs reads names and values in turn.
func pairs(parts []string) map[string]string {
	entries := make(map[string]string, len(parts)/2)
	for i := 0; i+1 < len(parts); i += 2 {
		entries[parts[i]] = parts[i+1]
	}

	return entries
}

func objectLiteral(p ir.Param, entries map[string]string) string {
	var fields []string

	for _, m := range p.Props {
		v, ok := entries[m.JSONName]
		if !ok {
			continue
		}

		lit := goLiteralForType(m, v)
		if m.Pointer {
			lit = newLiteral(m, v, lit)
		}

		fields = append(fields, m.FieldName+": "+lit)
	}

	if len(fields) == 0 {
		return ""
	}

	return p.Type + "{" + strings.Join(fields, ", ") + "}"
}

// newLiteral is a pointer to lit, the literal of the value v of m: of m's type, where lit is an untyped constant.
func newLiteral(m ir.Param, v, lit string) string {
	if lit == v || strings.HasPrefix(lit, `"`) {
		return fmt.Sprintf("new(%s(%s))", m.Type, lit)
	}

	return fmt.Sprintf("new(%s)", lit)
}

func mapLiteral(p ir.Param, entries map[string]string) string {
	if len(entries) == 0 {
		return ""
	}

	elems := make([]string, 0, len(entries))
	for _, k := range slices.Sorted(maps.Keys(entries)) {
		elems = append(elems, fmt.Sprintf("%q: %s", k, goLiteralForType(*p.MapValue, entries[k])))
	}

	return p.Type + "{" + strings.Join(elems, ", ") + "}"
}

// queryParts returns the parts of the value of the query parameter name in the query string raw, split at sep before
// each is unescaped, as the generated server's queryParts does.
func queryParts(raw, name, sep string) []string {
	for pair := range strings.SplitSeq(raw, "&") {
		k, v, _ := strings.Cut(pair, "=")
		if k, err := url.QueryUnescape(k); err != nil || k != name || v == "" {
			continue
		}

		parts := strings.Split(v, sep)
		for i, p := range parts {
			if u, err := url.QueryUnescape(p); err == nil {
				parts[i] = u
			}
		}

		return parts
	}

	return nil
}

// headerArgs are the literals of the header parameters h holds a value for.
func headerArgs(params ir.Params, h http.Header) []ir.InteractionParam {
	var args []ir.InteractionParam

	for _, hp := range params {
		if val := h.Get(hp.JSONName); val != "" {
			args = append(args, ir.InteractionParam{FieldName: hp.FieldName, Literal: goLiteralForType(hp, val)})
		}
	}

	return args
}

// setOutcome sets whether the call succeeded, and if not, the type of the error it returns, from the status recorded.
func setOutcome(call *ir.InteractionCall, op *ir.Operation, status int) {
	r := findResponse(op, status)
	if r == nil {
		// No declared response for this exact status code: fall back to the
		// HTTP convention so the generated assertion at least checks err==nil
		// vs err!=nil correctly, without asserting a specific error type.
		call.IsSuccess = status >= 200 && status < 300
		return
	}

	call.IsSuccess = r.IsSuccess

	switch {
	case r.IsSuccess:
	case r.IsRawBytes:
		// Nothing was decoded, so there is no generated type to match
		// on: the client hands the body back as an api.ErrorBody.
		call.ErrorType = "api.ErrorBody"
	case r.GoType != nil:
		call.ErrorType = r.GoType.String()
	}
}

// fillGlobalParamExamples gives every global parameter the specification has no
// example for the value that was recorded for it, so that the client default and
// the test environment agree with the cassette they are replayed against.
//
// The specification is not a reliable source here: an example is only present
// when the recorded value survived masking, so credentials tend to be exactly
// the parameters missing one.
func fillGlobalParamExamples(doc *ir.Document, interactions cassette.Interactions) {
	for i := range doc.GlobalParams {
		p := &doc.GlobalParams[i]
		if p.Example != "" {
			continue
		}

		for _, ia := range interactions {
			if val := ia.Request.Headers.Get(p.JSONName); val != "" {
				p.Example = strconv.Quote(val)
				break
			}
		}
	}
}

// findResponse returns the operation's declared response matching statusCode,
// or nil if the operation has no response entry for that exact numeric code.
func findResponse(op *ir.Operation, statusCode int) *ir.Response {
	for i := range op.Responses {
		r := &op.Responses[i]
		if n, err := strconv.Atoi(r.StatusCode); err == nil && n == statusCode {
			return r
		}
	}

	return nil
}

// pickOperation returns the operation whose method and path template best
// match the URL. An exact segment-count match beats a trailing-wildcard one,
// so /tasks/{taskId}/score/up wins over /tasks/{taskId} for
// /tasks/abc/score/up; among matches of the same kind, more literal segments
// win, as with net/http's ServeMux, so /things/running beats /things/{thingID}
// for /things/running. The path template breaks any remaining tie.
func pickOperation(ops []ir.Operation, method, relPath string) (*ir.Operation, map[string]string) {
	var (
		bestOp      *ir.Operation
		bestVals    map[string]string
		bestExact   bool
		bestLiteral int
	)
	for i := range ops {
		op := &ops[i]
		if op.Method != method {
			continue
		}

		vals, ok, exact := matchPathTemplate(op.PathTemplate, relPath)
		if !ok {
			continue
		}

		literal := literalSegments(op.PathTemplate)

		better := bestOp == nil
		switch {
		case better:
		case exact != bestExact:
			better = exact
		case literal != bestLiteral:
			better = literal > bestLiteral
		default:
			better = op.PathTemplate < bestOp.PathTemplate
		}

		if better {
			bestOp, bestVals, bestExact, bestLiteral = op, vals, exact, literal
		}
	}

	return bestOp, bestVals
}

// literalSegments counts the segments of a path template with no parameter in
// them.
func literalSegments(template string) int {
	n := 0
	for seg := range strings.SplitSeq(template, "/") {
		if !strings.Contains(seg, "{") {
			n++
		}
	}

	return n
}

// matchPathTemplate matches a URL path against an operation path template,
// returning the extracted parameter values keyed by parameter name.
// Template segments may be full-segment ({name}) or mid-segment (CIK{name}.json).
// A trailing pure {param} segment acts as a wildcard that consumes all remaining
// path segments joined by "/", enabling multi-segment captures like {path} in
// /package/{path} matching /package/github.com/user/repo. The third return
// value reports whether the match was segment-for-segment exact; callers should
// prefer an exact match over a wildcard one.
func matchPathTemplate(template, path string) (params map[string]string, matched, exact bool) {
	tParts := strings.Split(template, "/")
	pParts := strings.Split(path, "/")

	if len(tParts) > len(pParts) {
		return nil, false, false
	}

	params = make(map[string]string)
	for i, tp := range tParts {
		// Last template segment that is a pure {param} and there are extra path
		// segments: consume all remaining segments as one slash-joined value.
		if i == len(tParts)-1 &&
			strings.HasPrefix(tp, "{") &&
			strings.HasSuffix(tp, "}") &&
			strings.Count(tp, "{") == 1 &&
			len(pParts) > i+1 {
			params[tp[1:len(tp)-1]] = strings.Join(pParts[i:], "/")
			return params, true, false
		}

		if !extractSegmentParam(tp, pParts[i], params) {
			return nil, false, false
		}
	}

	sameLen := len(tParts) == len(pParts)

	return params, sameLen, sameLen
}

// extractSegmentParam matches one template segment against one path segment,
// populating out with any extracted parameter values. Returns false on mismatch.
func extractSegmentParam(tmpl, value string, out map[string]string) bool {
	if !strings.Contains(tmpl, "{") {
		return tmpl == value
	}

	// Simple case: entire segment is {name}
	if strings.HasPrefix(tmpl, "{") && strings.HasSuffix(tmpl, "}") && strings.Count(tmpl, "{") == 1 {
		out[tmpl[1:len(tmpl)-1]] = value
		return true
	}

	// Mid-segment case: e.g. "CIK{name}.json"
	start := strings.Index(tmpl, "{")
	end := strings.Index(tmpl, "}")
	if start < 0 || end <= start {
		return tmpl == value
	}

	prefix := tmpl[:start]
	suffix := tmpl[end+1:]
	name := tmpl[start+1 : end]

	if !strings.HasPrefix(value, prefix) || !strings.HasSuffix(value, suffix) {
		return false
	}

	extracted := value[len(prefix):]
	if len(suffix) > 0 {
		extracted = extracted[:len(extracted)-len(suffix)]
	}

	out[name] = extracted

	return true
}

// goLiteralForType returns a Go literal expression for value given a path,
// query, or header param, mirroring ir.Param.FormatExpr, which performs the
// same conversion in the other direction when the client builds the request.
// sliceLiteral is a Go literal of an array parameter holding values.
func sliceLiteral(p ir.Param, values []string) string {
	elems := make([]string, len(values))
	for i, v := range values {
		elems[i] = goLiteralForType(*p.Item, v)
	}

	return p.Type + "{" + strings.Join(elems, ", ") + "}"
}

func goLiteralForType(p ir.Param, value string) string {
	// a generated type takes the literal of the type it was declared from
	switch cmp.Or(p.BaseType, p.Type) {
	case "int", "int32", "int64", "uint", "uint32", "uint64", "float32", "float64", "bool":
		return value
	case "time.Duration":
		return value + " * time.Second"
	case "net.IP":
		return fmt.Sprintf("net.ParseIP(%q)", value)
	case "url.URL":
		if u, err := url.Parse(value); err == nil {
			return urlLiteral(u)
		}
	case "uuid.UUID":
		return fmt.Sprintf("uuid.MustParse(%q)", value)
	case "time.Time":
		// IsUnix: FormatExpr encoded the param as v.Unix(), so the
		// recorded value is a Unix timestamp, not an RFC 3339 string.
		if p.IsUnix {
			return fmt.Sprintf("time.Unix(%s, 0)", value)
		}

		if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return timeLiteral(t)
		}
	case "civil.Date":
		if p.IsUnix {
			if sec, err := strconv.ParseInt(value, 10, 64); err == nil {
				value = time.Unix(sec, 0).UTC().Format(time.DateOnly)
			}
		}

		if d, err := time.Parse(time.DateOnly, value); err == nil {
			return fmt.Sprintf("civil.Date{Year: %d, Month: %d, Day: %d}", d.Year(), d.Month(), d.Day())
		}
	}

	return fmt.Sprintf("%q", value)
}

// urlLiteral is a Go literal of u.
func urlLiteral(u *url.URL) string {
	var fields []string

	for _, f := range []struct{ name, value string }{
		{"Scheme", u.Scheme},
		{"Opaque", u.Opaque},
		{"Host", u.Host},
		{"Path", u.Path},
		{"RawQuery", u.RawQuery},
		{"Fragment", u.Fragment},
	} {
		if f.value != "" {
			fields = append(fields, fmt.Sprintf("%s: %q", f.name, f.value))
		}
	}

	return "url.URL{" + strings.Join(fields, ", ") + "}"
}

// timeLiteral is a Go expression for t that keeps its offset, so it formats back to what was recorded.
func timeLiteral(t time.Time) string {
	loc := "time.UTC"
	if _, offset := t.Zone(); offset != 0 {
		loc = fmt.Sprintf("time.FixedZone(\"\", %d)", offset)
	}

	return fmt.Sprintf("time.Date(%d, %d, %d, %d, %d, %d, %d, %s)",
		t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
}

// bodyLiteral returns a Go expression of type goType for the JSON value v (as
// decoded by json.Unmarshal into `any`: map[string]any, []any, string,
// float64, bool, or nil).
//
// It reproduces v as a composite literal wherever it recognizes the shape --
// a struct, an enum member, a slice, a pointer, or a plain scalar -- so the
// generated test reads like a value a person wrote, not a blob to decode.
// Anything it doesn't recognize (maps, unions, oneOf/anyOf, or a mismatch
// between v and what goType expects) falls back to decoding v's own compact
// JSON into goType at test time via mustDecodeBody, which is always correct
// even where a literal is not worth hand-rolling.
func bodyLiteral(doc *ir.Document, goType string, v any) string {
	switch {
	case v == nil:
		if strings.HasPrefix(goType, "*") {
			return "nil"
		}
		// A required value with nothing recorded for it: fall through to the
		// zero-value composite literal below rather than emit invalid Go.

	case strings.HasPrefix(goType, "*"):
		return fmt.Sprintf("new(%s)", bodyLiteral(doc, goType[1:], v))

	case strings.HasPrefix(goType, "[]"):
		arr, ok := v.([]any)
		if !ok {
			break
		}

		elems := make([]string, len(arr))
		for i, e := range arr {
			elems[i] = bodyLiteral(doc, goType[2:], e)
		}

		return fmt.Sprintf("%s{%s}", goType, strings.Join(elems, ", "))
	}

	if s := findSchema(doc, goType); s != nil {
		switch s.Kind {
		case ir.SchemaKindStruct, ir.SchemaKindAllOf:
			obj, isObj := v.(map[string]any)
			hasEmbedded := slices.ContainsFunc(s.Fields, func(f ir.Field) bool { return f.Embedded })

			// A non-object value where an object was expected, or a schema
			// with an allOf-embedded field (Field.Name is empty there, so it
			// has no JSON key of its own to look values up by): not worth
			// hand-rolling, fall through to the fallback below instead.
			if isObj && !hasEmbedded {
				var fields []string
				for _, f := range s.Fields {
					val, ok := obj[f.JSONName]
					if !ok {
						continue // absent optional field: leave it at its zero value
					}

					fields = append(fields, fmt.Sprintf("%s: %s", f.Name, bodyLiteral(doc, f.Type, val)))
				}

				if len(fields) == 0 {
					return goType + "{}"
				}

				return fmt.Sprintf("%s{\n%s,\n}", goType, strings.Join(fields, ",\n"))
			}

		case ir.SchemaKindEnum:
			if lit, ok := enumLiteral(s, v); ok {
				return lit
			}
		case ir.SchemaKindAlias:
			// a named scalar takes the scalar's literal, converted unless it is an alias
			if lit, ok := scalarLiteral(s.Type, v); ok {
				if s.IsTypeAlias {
					return lit
				}

				return fmt.Sprintf("%s(%s)", goType, lit)
			}
		default:
		}

		// SchemaKindMap, SchemaKindUnion, an alias of no scalar, or an enum value
		// that didn't match any declared member: not worth hand-rolling.
	}

	if lit, ok := scalarLiteral(goType, v); ok {
		return lit
	}

	return fmt.Sprintf("mustDecodeBody[%s](t, %s)", goType, fallbackJSON(v))
}

// findSchema returns doc's named component schema for goType, or nil if
// goType isn't the bare name of one (a built-in type, or a pointer/slice
// expression already stripped by the caller).
func findSchema(doc *ir.Document, goType string) *ir.Schema {
	for i := range doc.Schemas {
		if doc.Schemas[i].Name == goType {
			return &doc.Schemas[i]
		}
	}

	return nil
}

// enumLiteral returns the Go constant for the enum member of s matching v,
// formatted the same way ir.formatEnumValue derived each member's display
// form from the spec, so the comparison lines up exactly.
func enumLiteral(s *ir.Schema, v any) (string, bool) {
	var display string
	switch s.Type {
	case "string":
		str, ok := v.(string)
		if !ok {
			return "", false
		}

		display = str
	case "bool":
		b, ok := v.(bool)
		if !ok {
			return "", false
		}

		display = strconv.FormatBool(b)
	default: // int-family or float-family
		f, ok := v.(float64)
		if !ok {
			return "", false
		}

		if strings.HasPrefix(s.Type, "float") {
			display = strconv.FormatFloat(f, 'g', -1, 64)
		} else {
			display = strconv.FormatInt(int64(f), 10)
		}
	}

	for _, ev := range s.EnumValues {
		if ev.Value == display {
			return ev.GoName, true
		}
	}

	return "", false
}

// scalarLiteral returns a Go literal for v as a built-in or well-known
// wrapper type, or ok=false when goType isn't one it knows how to express
// directly.
func scalarLiteral(goType string, v any) (string, bool) {
	switch goType {
	case "string":
		s, ok := v.(string)
		if !ok {
			return "", false
		}

		return fmt.Sprintf("%q", s), true

	case "bool":
		b, ok := v.(bool)
		if !ok {
			return "", false
		}

		return strconv.FormatBool(b), true

	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64":
		f, ok := v.(float64)
		if !ok {
			return "", false
		}
		// An untyped integer constant assigns directly to any of these
		// field types, so no conversion is needed regardless of goType.
		return strconv.FormatInt(int64(f), 10), true

	case "float32", "float64":
		f, ok := v.(float64)
		if str, isStr := v.(string); isStr { // a number sent as a string, see ir.Field.IsStringOrNumber
			var err error

			f, err = strconv.ParseFloat(str, 64)
			ok = err == nil
		}

		if !ok {
			return "", false
		}

		return strconv.FormatFloat(f, 'g', -1, 64), true

	case "uuid.UUID":
		s, ok := v.(string)
		if !ok {
			return "", false
		}

		return fmt.Sprintf("uuid.MustParse(%q)", s), true

	case "time.Time":
		switch v := v.(type) {
		case float64: // an integer of unix time
			return fmt.Sprintf("time.Unix(%d, 0)", int64(v)), true
		case string:
			if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return timeLiteral(t), true
			}
		}

		return "", false

	case "civil.Date":
		var d time.Time

		switch v := v.(type) {
		case float64: // an integer of unix time
			d = time.Unix(int64(v), 0).UTC()
		case string:
			var err error
			if d, err = time.Parse(time.DateOnly, v); err != nil {
				if d, err = time.Parse("20060102", v); err != nil { // ISO 8601's basic form, see ir.Field.IsDateBasic
					return "", false
				}
			}
		default:
			return "", false
		}

		return fmt.Sprintf("civil.Date{Year: %d, Month: %d, Day: %d}", d.Year(), d.Month(), d.Day()), true

	default:
		return "", false
	}
}

// fallbackJSON returns v's compact JSON encoding as a quoted Go string
// literal, for embedding in a mustDecodeBody call.
func fallbackJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		// v was itself decoded from JSON moments ago, so re-encoding it
		// cannot fail; this is unreachable in practice.
		return fmt.Sprintf("%q", "null")
	}

	return fmt.Sprintf("%q", string(data))
}
