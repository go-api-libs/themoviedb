package ir

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/MarkRosemaker/openapi"
)

// setStyle sets how the query parameter p is written from its style and explode, defaulting to form, exploded.
//
// A single value is written alike in every style. An array or an object is written as one value of the parameter,
// its elements, or its members' names and values, joined by the style's delimiter, unless it is exploded: then each
// element is a value of the parameter of its own, and each member a parameter of its own, under its name, or with
// deepObject under the parameter's name with the member's in brackets.
func (param *Param) setStyle(p *openapi.Parameter) {
	param.AllowReserved = p.AllowReserved

	if param.Item == nil && param.Props == nil && param.MapValue == nil {
		return
	}

	style := cmp.Or(p.Style, openapi.ParameterStyleForm)

	explode := style == openapi.ParameterStyleForm
	if p.Explode != nil {
		explode = *p.Explode
	}

	switch {
	case style == openapi.ParameterStyleDeepObject:
		// an array has no members to put in brackets, so it is written as form, exploded, would
		param.DeepObject = param.Item == nil
	case explode:
	case style == openapi.ParameterStyleSpaceDelimited:
		param.Delimiter = "%20"
	case style == openapi.ParameterStylePipeDelimited:
		param.Delimiter = "|"
	default:
		param.Delimiter = ","
	}
}

// setObject makes param an object parameter of schema s: a struct of its properties, or a map if it declares none.
func (param *Param) setObject(s *openapi.Schema) error {
	tp, err := SchemaGoType(s)
	if err != nil {
		return err
	}

	param.Type = tp.String()

	d := deref(s)
	if len(d.Properties) == 0 {
		param.MapValue = &Param{VarName: "v"}

		ap := d.AdditionalProperties
		if ap == nil || ap.Schema == nil {
			// free-form: whatever the query holds, as it reads
			param.Type = "map[string]string"
			return param.MapValue.setType(&openapi.Schema{Type: openapi.TypeString})
		}

		if err := param.MapValue.setType(paramSchema(ap.Schema)); err != nil {
			return fmt.Errorf("additionalProperties: %w", err)
		}

		return nil
	}

	required := make(map[string]bool, len(d.Required))
	for _, r := range d.Required {
		required[r] = true
	}

	for name, prop := range d.Properties.ByIndex() {
		if t := deref(prop).Type; t == openapi.TypeArray || t == openapi.TypeObject {
			return fmt.Errorf("property %q: a member of a query parameter must be a single value, not an %s", name, t)
		}

		// the member's field, as the struct declares it
		f, err := getField(name, prop, required)
		if err != nil {
			return fmt.Errorf("property %q: %w", name, err)
		}

		member := Param{JSONName: name, FieldName: f.Name, Required: f.Required, Pointer: strings.HasPrefix(f.Type, "*")}
		if err := member.setType(paramSchema(prop)); err != nil {
			return fmt.Errorf("property %q: %w", name, err)
		}

		param.Props = append(param.Props, member)
	}

	return nil
}

// escapes reports whether p writes a value the client escapes itself: one of several joined by a delimiter, which
// must not be escaped, or one whose reserved characters are left as they are.
func (p Param) escapes() bool { return p.Delimiter != "" || p.AllowReserved }

// EscapesQuery reports whether a query parameter of the operation is written by encodeQuery instead of
// [url.Values.Encode].
func (op Operation) EscapesQuery() bool { return slices.ContainsFunc(op.QueryParams, Param.escapes) }

// EncodeQuery is the client's code that writes the operation's query parameters into the query of the URL u.
func (op Operation) EncodeQuery() string {
	var b strings.Builder

	fmt.Fprintf(&b, "q := make(url.Values, %d)\n", len(op.QueryParams))

	if op.EscapesQuery() {
		b.WriteString("escaped := url.Values{}\n")
	}

	for _, p := range op.QueryParams {
		b.WriteString("\n" + p.encodeQuery())
	}

	if op.EscapesQuery() {
		b.WriteString("\nu.RawQuery = encodeQuery(q, escaped)")
	} else {
		b.WriteString("\nu.RawQuery = q.Encode()")
	}

	return b.String()
}

// encodeQuery is the client's code that writes p into the query values q, or escaped.
func (p Param) encodeQuery() string {
	name := strconv.Quote(p.JSONName)

	switch {
	case p.MapValue != nil:
		return p.encodeMap(name)
	case p.Props != nil:
		return p.encodeObject(name)
	case p.Item == nil:
		return guard(p, p.put(name, p.FormatExpr()))
	case p.Delimiter == "":
		return fmt.Sprintf("for _, v := range %s {\n%s}\n", p.VarName, p.add(name, p.Item.FormatExpr()))
	case p.Item.Type == "string":
		return fmt.Sprintf("if len(%[1]s) > 0 {\nescaped[%[2]s] = []string{%[3]s}\n}\n",
			p.VarName, name, p.join("slices.Clone("+p.VarName+")"))
	default:
		return fmt.Sprintf("if len(%[1]s) > 0 {\nparts := make([]string, len(%[1]s))\nfor i, v := range %[1]s {\n"+
			"parts[i] = %[2]s\n}\n\nescaped[%[3]s] = []string{%[4]s}\n}\n",
			p.VarName, p.Item.FormatExpr(), name, p.join("parts"))
	}
}

func (p Param) encodeObject(name string) string {
	var b strings.Builder

	if p.Delimiter != "" {
		b.WriteString("var parts []string\n")
	}

	for _, m := range p.Props {
		var stmt string

		switch {
		case p.Delimiter != "":
			stmt = fmt.Sprintf("parts = append(parts, %q, %s)\n", m.JSONName, m.FormatExpr())
		case p.DeepObject:
			stmt = p.put(strconv.Quote(p.JSONName+"["+m.JSONName+"]"), m.FormatExpr())
		default:
			stmt = p.put(strconv.Quote(m.JSONName), m.FormatExpr())
		}

		b.WriteString(guard(m, stmt))
	}

	if p.Delimiter != "" {
		fmt.Fprintf(&b, "\nescaped[%s] = []string{%s}\n", name, p.join("parts"))
	}

	if p.Required {
		return "{\n" + b.String() + "}\n"
	}

	// an object not set sends nothing, not even the members it requires
	set := make([]string, len(p.Props))
	for i, m := range p.Props {
		set[i] = m.NotZero()
	}

	return fmt.Sprintf("if %s {\n%s}\n", strings.Join(set, " || "), b.String())
}

func (p Param) encodeMap(name string) string {
	key := "k"
	val := p.MapValue.FormatExpr()

	switch {
	case p.Delimiter != "":
		return fmt.Sprintf("if len(%[1]s) > 0 {\nparts := make([]string, 0, 2*len(%[1]s))\n"+
			"for _, k := range slices.Sorted(maps.Keys(%[1]s)) {\nv := %[1]s[k]\nparts = append(parts, %[2]s, %[3]s)\n}\n\n"+
			"escaped[%[4]s] = []string{%[5]s}\n}\n", p.VarName, key, val, name, p.join("parts"))
	case p.DeepObject:
		key = strconv.Quote(p.JSONName+"[") + " + " + key + ` + "]"`
	}

	return fmt.Sprintf("for k, v := range %s {\n%s}\n", p.VarName, p.put(key, val))
}

// put is the client's code that makes val the value of the query parameter key.
func (p Param) put(key, val string) string {
	if p.AllowReserved {
		return fmt.Sprintf("escaped[%s] = []string{escapeReserved(%s)}\n", key, val)
	}

	return fmt.Sprintf("q[%s] = []string{%s}\n", key, val)
}

// add is the client's code that adds val to the values of the query parameter key.
func (p Param) add(key, val string) string {
	if p.AllowReserved {
		return fmt.Sprintf("escaped.Add(%s, escapeReserved(%s))\n", key, val)
	}

	return fmt.Sprintf("q.Add(%s, %s)\n", key, val)
}

// join is the client's expression that escapes each of parts and joins them by p's delimiter.
func (p Param) join(parts string) string {
	escape := "url.QueryEscape"
	if p.AllowReserved {
		escape = "escapeReserved"
	}

	return fmt.Sprintf("joinQuery(%s, %q, %s)", parts, p.Delimiter, escape)
}

// guard is stmt, run only when p is set if it need not be.
func guard(p Param, stmt string) string {
	if p.Required && !p.Pointer {
		return stmt
	}

	return fmt.Sprintf("if %s {\n%s}\n", p.NotZero(), stmt)
}

// ParseArgsFunc is the name of the server's function that reads the operation's path and query parameters from a
// request, if any can fail to parse: path parameters that cannot are read in place, by ParsePath.
func (op Operation) ParseArgsFunc() string {
	if len(op.QueryParams) == 0 && !slices.ContainsFunc(op.PathParams, func(p Param) bool { return !p.ParseErrFree }) {
		return ""
	}

	return "parse" + op.Name + "Args"
}

// ParsePath is the server's expression that reads the path parameter p, which cannot fail to parse, from r.
func (p Param) ParsePath() string {
	return fmt.Sprintf(p.ParseExpr, fmt.Sprintf("r.PathValue(%q)", p.JSONName))
}

// ParseArgsResults are the variables ParseArgsFunc returns, the error aside.
func (op Operation) ParseArgsResults() string {
	names := make([]string, 0, len(op.PathParams)+1)
	for _, p := range op.PathParams {
		names = append(names, p.GoName)
	}

	if len(op.QueryParams) > 0 {
		names = append(names, "params")
	}

	return strings.Join(names, ", ")
}

// ParseArgs is the server's function named ParseArgsFunc.
func (op Operation) ParseArgs() string {
	results := op.ParseArgsResults()
	fail := "return " + results + ", %s"

	var b strings.Builder

	fmt.Fprintf(&b, "// %s reads the path and query parameters of %s from req.\nfunc %s(req *http.Request) (",
		op.ParseArgsFunc(), op.Name, op.ParseArgsFunc())

	for _, p := range op.PathParams {
		fmt.Fprintf(&b, "%s %s, ", p.GoName, p.Type)
	}

	if len(op.QueryParams) > 0 {
		fmt.Fprintf(&b, "params %s, ", op.ParamStructName)
	}

	b.WriteString("err error) {\n")

	for _, p := range op.PathParams {
		stmts := p.parseStmts(p.JSONName, fmt.Sprintf("req.PathValue(%q)", p.JSONName), p.GoName+" = %s", fail)
		if !p.ParseErrFree {
			stmts = "{\n" + stmts + "}\n"
		}

		b.WriteString(stmts + "\n")
	}

	if slices.ContainsFunc(op.QueryParams, func(p Param) bool { return p.Delimiter == "" }) {
		b.WriteString("query := req.URL.Query()\n")
	}

	for _, p := range op.QueryParams {
		b.WriteString("\n" + p.parseQuery(op, fail))
	}

	fmt.Fprintf(&b, "return %s, nil\n}", results)

	return b.String()
}

// OtherQueryNames are the names of the query parameters of the operation that the exploded map parameter mp does
// not hold: every other's, an exploded object's members' and the fixed ones'; and the prefixes of those written as
// deepObject, which are followed by a member's name in brackets.
func (op Operation) OtherQueryNames(mp Param) (names, prefixes []string) {
	for _, p := range op.QueryParams {
		switch {
		case p.JSONName == mp.JSONName:
		case p.DeepObject:
			prefixes = append(prefixes, p.JSONName+"[")
		case p.Props != nil && p.Delimiter == "":
			for _, m := range p.Props {
				names = append(names, m.JSONName)
			}
		default:
			names = append(names, p.JSONName)
		}
	}

	for _, p := range op.FixedParams {
		if p.In == "query" {
			names = append(names, p.JSONName)
		}
	}

	return names, prefixes
}

// parseStmts is the server's code that parses src into p's Go type and hands the value to set, a format; a value
// that does not parse is returned as the error fail is a format for, naming the parameter as name.
func (p Param) parseStmts(name, src, set, fail string) string {
	expr := fmt.Sprintf(p.ParseExpr, src)
	if p.ParseErrFree {
		return fmt.Sprintf(set, expr) + "\n"
	}

	val := "v"
	if p.ParseConv != "" {
		val = fmt.Sprintf(p.ParseConv, val)
	}

	return fmt.Sprintf("v, err := %s\nif err != nil {\n%s\n}\n\n%s\n", expr, failInvalid(fail, name, "err"), fmt.Sprintf(set, val))
}

// failInvalid is fail with the error that the query parameter name is invalid for err.
func failInvalid(fail, name, err string) string {
	return fmt.Sprintf(fail, fmt.Sprintf("fmt.Errorf(%q, %s)", "invalid "+name+": %w", err))
}

// set is the format of the server's code that sets p to a value.
func (p Param) set() string {
	if p.Pointer {
		return p.VarName + " = new(%s)"
	}

	return p.VarName + " = %s"
}

// parts is the server's code that reads the parts of p's one value into parts, its delimiter between them.
func (p Param) parts(fail string) string {
	return fmt.Sprintf("parts, err := queryParts(req.URL.RawQuery, %q, %q)\nif err != nil {\n%s\n}\n",
		p.JSONName, p.Delimiter, failInvalid(fail, p.JSONName, "err"))
}

// pairs is the server's code that reads names and values from parts, their name in the variable name and their value
// in s, handing each to stmts.
func (p Param) pairs(fail, name, stmts string) string {
	notPairs := fmt.Sprintf("errors.New(%q)", "invalid "+p.JSONName+": not names and values in turn")

	return fmt.Sprintf("if len(parts)%%2 != 0 {\n%s\n}\n\nfor i := 0; i < len(parts); i += 2 {\n%s, s := parts[i], parts[i+1]\n%s}\n",
		fmt.Sprintf(fail, notPairs), name, stmts)
}

// parseQuery is the server's code that reads p, a query parameter of op, from the query values query, or from the
// request's query string.
func (p Param) parseQuery(op Operation, fail string) string {
	switch {
	case p.MapValue != nil:
		return p.parseMap(op, fail)
	case p.Props != nil:
		return p.parseObject(fail)
	case p.Item == nil:
		return fmt.Sprintf("if s := query.Get(%q); s != \"\" {\n%s}\n", p.JSONName, p.parseStmts(p.JSONName, "s", p.set(), fail))
	}

	values := fmt.Sprintf("query[%q]", p.JSONName)
	if p.Delimiter != "" {
		values = "parts"
	}

	stmts := fmt.Sprintf("for _, s := range %s {\n%s}\n", values,
		p.Item.parseStmts(p.JSONName, "s", p.VarName+" = append("+p.VarName+", %s)", fail))
	if p.Item.Type == "string" {
		stmts = p.VarName + " = " + values + "\n"
	}

	if p.Delimiter == "" {
		return stmts
	}

	return fmt.Sprintf("{\n%s\n%s}\n", p.parts(fail), stmts)
}

func (p Param) parseObject(fail string) string {
	var b strings.Builder

	if p.Delimiter != "" {
		for _, m := range p.Props {
			fmt.Fprintf(&b, "case %q:\n%s", m.JSONName, m.parseStmts(p.JSONName+"."+m.JSONName, "s", m.set(), fail))
		}

		return fmt.Sprintf("{\n%s\n%s}\n", p.parts(fail), p.pairs(fail, "name", "switch name {\n"+b.String()+"}\n"))
	}

	for _, m := range p.Props {
		key := m.JSONName
		if p.DeepObject {
			key = p.JSONName + "[" + key + "]"
		}

		fmt.Fprintf(&b, "if s := query.Get(%q); s != \"\" {\n%s}\n", key, m.parseStmts(key, "s", m.set(), fail))
	}

	return b.String()
}

func (p Param) parseMap(op Operation, fail string) string {
	set := fmt.Sprintf("if %[1]s == nil {\n%[1]s = %[2]s{}\n}\n\n%[1]s[k] = %%s", p.VarName, p.Type)
	stmts := p.MapValue.parseStmts(p.JSONName, "s", set, fail)

	switch {
	case p.Delimiter != "":
		return fmt.Sprintf("{\n%s\n%s}\n", p.parts(fail), p.pairs(fail, "k", stmts))
	case p.DeepObject:
		return fmt.Sprintf("for k, vs := range query {\nk, ok := strings.CutPrefix(k, %q)\nif !ok {\ncontinue\n}\n\n"+
			"k, ok = strings.CutSuffix(k, \"]\")\nif !ok {\ncontinue\n}\n\ns := vs[0]\n%s}\n", p.JSONName+"[", stmts)
	}

	names, prefixes := op.OtherQueryNames(p)

	var skip string
	if len(names) > 0 {
		quoted := make([]string, len(names))
		for i, n := range names {
			quoted[i] = strconv.Quote(n)
		}

		skip = fmt.Sprintf("switch k {\ncase %s:\ncontinue\n}\n\n", strings.Join(quoted, ", "))
	}

	for _, prefix := range prefixes {
		skip += fmt.Sprintf("if strings.HasPrefix(k, %q) {\ncontinue\n}\n\n", prefix)
	}

	return fmt.Sprintf("for k, vs := range query {\n%ss := vs[0]\n%s}\n", skip, stmts)
}

// JSQuery is api.js's code that writes the operation's query parameters: into the URLSearchParams q, or, escaped
// already, into raw.
func (op Operation) JSQuery() string {
	var b strings.Builder

	for _, p := range op.QueryParams {
		b.WriteString(p.jsQuery())
	}

	return b.String()
}

// JSQueryString is api.js's expression of the query string JSQuery writes.
func (op Operation) JSQueryString() string {
	if op.EscapesQuery() {
		return `[q.toString(), ...raw].filter(Boolean).join("&")`
	}

	return "q"
}

func (p Param) jsQuery() string {
	v := "params." + p.GoName

	esc := "esc"
	if p.AllowReserved {
		esc = "escR"
	}

	// add is the code that adds the value val under the name, a JS expression, key
	add := func(key, val string) string {
		if p.AllowReserved {
			return fmt.Sprintf("raw.push(esc(%s) + \"=\" + escR(%s));", key, val)
		}

		return fmt.Sprintf("q.append(%s, String(%s));", key, val)
	}

	name := strconv.Quote(p.JSONName)
	joined := func(parts string) string {
		return fmt.Sprintf("raw.push(%q + join(%s, %q, %s));", url.QueryEscape(p.JSONName)+"=", parts, p.Delimiter, esc)
	}

	switch {
	case p.Props != nil || p.MapValue != nil:
		entries := fmt.Sprintf("Object.entries(%s ?? {}).filter(([, v]) => v != null)", v)

		switch {
		case p.Delimiter != "":
			return fmt.Sprintf("\n        { const parts = %s.flat(); if (parts.length) %s }", entries, joined("parts"))
		case p.DeepObject:
			return fmt.Sprintf("\n        for (const [k, v] of %s) %s", entries, add(fmt.Sprintf("`%s[${k}]`", p.JSONName), "v"))
		default:
			return fmt.Sprintf("\n        for (const [k, v] of %s) %s", entries, add("k", "v"))
		}
	case p.Item == nil:
		if p.AllowReserved {
			return fmt.Sprintf("\n        if (%s != null) %s", v, add(name, v))
		}

		return fmt.Sprintf("\n        if (%s != null) q.set(%s, String(%s));", v, name, v)
	case p.Delimiter == "":
		return fmt.Sprintf("\n        for (const v of %s ?? []) %s", v, add(name, "v"))
	default:
		return fmt.Sprintf("\n        if (%s?.length) %s", v, joined(v))
	}
}

// EscapesQuery reports whether an operation writes its query with the helpers that escape it themselves.
func (doc Document) EscapesQuery() bool {
	return slices.ContainsFunc(doc.Operations, Operation.EscapesQuery)
}

// AllowsReserved reports whether a query parameter leaves reserved characters as they are.
func (doc Document) AllowsReserved() bool {
	return doc.anyQueryParam(func(p Param) bool { return p.AllowReserved })
}

// JoinsQuery reports whether a query parameter is sent as values joined by a delimiter, which the server splits.
func (doc Document) JoinsQuery() bool {
	return doc.anyQueryParam(func(p Param) bool { return p.Delimiter != "" })
}

func (doc Document) anyQueryParam(f func(Param) bool) bool {
	return slices.ContainsFunc(doc.Operations, func(op Operation) bool { return slices.ContainsFunc(op.QueryParams, f) })
}
