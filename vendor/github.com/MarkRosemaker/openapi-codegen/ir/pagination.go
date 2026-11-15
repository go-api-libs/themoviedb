package ir

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"slices"
	"strings"
)

// Pagination is how an operation's list is paged through by cursor: the client sends the cursor of the page it wants,
// none for the first, and the list says the cursor of the next, and whether there is one. The client gets an
// iterator of the pages and one of their items.
type Pagination struct {
	// CursorType is the Go type of the cursor, which the list's NextField holds.
	CursorType string `json:"cursorType,omitzero"`
	// Prepare is the Go statements that put the cursor, as cursor, into the request, copying what the caller passed
	// rather than changing it; Args is the arguments the operation is then called with, after its context.
	Prepare string `json:"prepare,omitzero"`
	Args    string `json:"args,omitzero"`
	// NextField and MoreField are the fields of the list holding the cursor of the next page and whether there is
	// one; without the latter, there is one if the cursor is not zero.
	NextField string `json:"nextField,omitzero"`
	MoreField string `json:"moreField,omitzero"`
	// ItemsField is the field of the list holding its items, each of ItemType.
	ItemsField string `json:"itemsField,omitzero"`
	ItemType   string `json:"itemType,omitzero"`
}

// The names a list's members and the cursor a request sends are found by, where x-pagination names none.
var (
	cursorNames = []string{"start_cursor", "cursor", "page_token", "pageToken"}
	nextNames   = []string{"next_cursor", "nextCursor", "next_page_token", "nextPageToken"}
	moreNames   = []string{"has_more", "hasMore", "has_next_page"}
	itemsNames  = []string{"results", "data", "items"}
)

// paginationExt is what an operation's x-pagination says: the JSON names of the cursor the request sends and of the
// list's members, any of which may be left to be found, or that the operation is not paged through, as false.
type paginationExt struct {
	set, off                  bool
	Cursor, Next, More, Items string
}

// readPaginationExt reads the x-pagination of an operation's extensions.
func readPaginationExt(ext jsontext.Value) (paginationExt, error) {
	if len(ext) == 0 {
		return paginationExt{}, nil
	}

	var all map[string]jsontext.Value
	if err := json.Unmarshal(ext, &all); err != nil {
		return paginationExt{}, err
	}

	raw, ok := all["x-pagination"]
	if !ok {
		return paginationExt{}, nil
	}

	if string(raw) == "false" {
		return paginationExt{set: true, off: true}, nil
	}

	var p struct {
		Cursor string `json:"cursor"`
		Next   string `json:"next"`
		More   string `json:"more"`
		Items  string `json:"items"`
	}
	if err := json.Unmarshal(raw, &p, json.RejectUnknownMembers(true)); err != nil {
		return paginationExt{}, fmt.Errorf("x-pagination: %w", err)
	}

	return paginationExt{set: true, Cursor: p.Cursor, Next: p.Next, More: p.More, Items: p.Items}, nil
}

// paginate gives each operation whose list is paged through by cursor its Pagination: what its x-pagination names,
// or else what is found by the names lists and cursors commonly have. One x-pagination names but does not fit fails.
func paginate(ops []Operation, schemas []Schema) error {
	byName := make(map[string]*Schema, len(schemas))
	for i := range schemas {
		byName[schemas[i].Name] = &schemas[i]
	}

	taken := map[string]bool{}
	for _, op := range ops {
		taken[op.Name] = true
	}

	for i := range ops {
		op := &ops[i]
		if op.pagination.off {
			continue
		}

		p, err := pagination(op, byName)
		if err == nil && (taken[op.Name+"Pages"] || taken[op.Name+"All"]) {
			err = fmt.Errorf("%sPages or %sAll is the name of an operation", op.Name, op.Name)
		}

		switch {
		case err == nil:
			op.Pagination = p
		case op.pagination.set:
			return fmt.Errorf("%s: x-pagination: %w", op.Name, err)
		}
	}

	return nil
}

// pagination is how op's list is paged through, or why it cannot be.
func pagination(op *Operation, byName map[string]*Schema) (*Pagination, error) {
	ext := op.pagination

	if op.SuccessReturn == nil || op.RawBytesSuccess || op.StreamSuccess {
		return nil, fmt.Errorf("it answers with no list")
	}

	list := byName[op.SuccessReturn.Name]
	if list == nil || list.Kind != SchemaKindStruct {
		return nil, fmt.Errorf("it answers with no object")
	}

	items := fieldOf(list, ext.Items, itemsNames)
	if items == nil {
		return nil, fmt.Errorf("%s has no list of items", list.Name)
	}

	itemsType := items.Type
	if s := byName[itemsType]; s != nil && s.Kind == SchemaKindAlias {
		itemsType = s.Type // a named list, as type Templates []Template
	}

	if !strings.HasPrefix(itemsType, "[]") {
		return nil, fmt.Errorf("%s has no list of items", list.Name)
	}

	next := fieldOf(list, ext.Next, nextNames)
	if next == nil || strings.HasPrefix(next.Type, "*") {
		return nil, fmt.Errorf("%s has no cursor of the next page", list.Name)
	}

	more := fieldOf(list, ext.More, moreNames)
	if more != nil && more.Type != "bool" {
		return nil, fmt.Errorf("%s.%s is no bool", list.Name, more.Name)
	}

	p := &Pagination{
		CursorType: next.Type, NextField: next.Name, ItemsField: items.Name, ItemType: strings.TrimPrefix(itemsType, "[]"),
	}
	if more != nil {
		p.MoreField = more.Name
	}

	// the cursor is a query parameter or a member of the body
	cursorNames := cursorNames
	if ext.Cursor != "" {
		cursorNames = []string{ext.Cursor}
	}

	set := func(holder, field, typ string) error {
		switch typ {
		case next.Type, aliasOf(byName, next.Type):
			p.Prepare = holder + "." + field + " = cursor"
		case "*" + next.Type:
			p.Prepare = holder + "." + field + " = &cursor"
		default:
			return fmt.Errorf("the cursor it takes is a %s, the one its list gives a %s", typ, next.Type)
		}

		return nil
	}

	args := op.CallArgs()
	p.Args = args

	if i := slices.IndexFunc(op.QueryParams, func(q Param) bool { return slices.Contains(cursorNames, q.JSONName) }); i >= 0 {
		q := op.QueryParams[i]

		if op.ParamsInStruct().Required() {
			return p, set("params", q.FieldName, q.Type)
		}

		if err := set("p", q.FieldName, q.Type); err != nil {
			return nil, err
		}

		p.Prepare = "var p " + op.ParamStructName + "\nif params != nil {\np = *params\n}\n" + p.Prepare
		p.Args = strings.Replace(args, ", params", ", &p", 1)

		return p, nil
	}

	if op.RequestBody != nil {
		body := byName[strings.TrimPrefix(op.RequestBody.TypeName, "*")]
		if f := fieldOf(body, ext.Cursor, cursorNames); body != nil && f != nil {
			if op.RequestBody.Required {
				return p, set("body", f.Name, f.Type)
			}

			if err := set("b", f.Name, f.Type); err != nil {
				return nil, err
			}

			p.Prepare = "var b " + op.RequestBody.TypeName + "\nif body != nil {\nb = *body\n}\n" + p.Prepare
			p.Args = strings.Replace(args, ", body", ", &b", 1)

			return p, nil
		}
	}

	return nil, fmt.Errorf("it takes no cursor")
}

// fieldOf is the field of s whose JSON name is name, or, where name is empty, the first of names; nil for none.
func fieldOf(s *Schema, name string, names []string) *Field {
	if s == nil {
		return nil
	}

	if name != "" {
		names = []string{name}
	}

	for _, n := range names {
		if i := slices.IndexFunc(s.Fields, func(f Field) bool { return f.JSONName == n }); i >= 0 {
			return &s.Fields[i]
		}
	}

	return nil
}

// aliasOf is the type the schema named name is an alias of, or "" if it is none.
func aliasOf(byName map[string]*Schema, name string) string {
	if s := byName[name]; s != nil && s.IsTypeAlias {
		return s.Type
	}

	return ""
}

// Zero is the zero value of the cursor, which a list without a MoreField ends with.
func (p Pagination) Zero() string {
	if p.CursorType == "string" {
		return `""`
	}

	return "*new(" + p.CursorType + ")"
}

// ArgsDecl is the parameters of the operation's method after its context, as declared.
func (op Operation) ArgsDecl() string {
	var b strings.Builder

	for _, p := range op.PathParams {
		b.WriteString(", " + p.GoName + " " + p.Type)
	}

	if op.ParamsInStruct() != nil {
		b.WriteString(", params ")

		if !op.ParamsInStruct().Required() {
			b.WriteString("*")
		}

		b.WriteString(op.ParamStructName)
	}

	if op.RequestBody != nil {
		b.WriteString(", body ")

		if !op.RequestBody.Required {
			b.WriteString("*")
		}

		b.WriteString(op.RequestBody.TypeName)
	}

	return b.String()
}

// CallArgs is the arguments the operation's method is called with after its context, as ArgsDecl names them.
func (op Operation) CallArgs() string {
	var b strings.Builder

	for _, p := range op.PathParams {
		b.WriteString(", " + p.GoName)
	}

	if op.ParamsInStruct() != nil {
		b.WriteString(", params")
	}

	if op.RequestBody != nil {
		b.WriteString(", body")
	}

	return b.String()
}
