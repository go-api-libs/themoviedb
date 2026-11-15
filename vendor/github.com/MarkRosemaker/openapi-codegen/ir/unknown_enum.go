package ir

import (
	"strconv"
	"strings"
)

// enums tells what a Go type of the document holds of an enum, so that debug mode can record a response holding a
// value the specification does not list: it decodes, since an enum is a plain string or number, but is news.
type enums struct {
	byName map[string]*Schema
	zero   map[string]string // the zero value of each enum type, which an optional member holds while left out
	holds  map[string]bool   // whether a named type holds an enum, as found so far
}

func newEnums(schemas []Schema) *enums {
	e := &enums{byName: make(map[string]*Schema, len(schemas)), zero: map[string]string{}, holds: map[string]bool{}}

	for i := range schemas {
		s := &schemas[i]
		e.byName[s.Name] = s

		if s.Kind == SchemaKindEnum {
			e.zero[s.Name] = zeroOf(s.Type)
		}

		if s.Tagged != nil && len(s.Tagged.Enum) > 0 {
			e.zero[s.Tagged.Type] = `""`
		}
	}

	return e
}

func zeroOf(typ string) string {
	if typ == "string" {
		return `""`
	}

	return "0"
}

// markUnknownEnums gives each schema that holds an enum, if not through a type alias, its UnknownEnum.
func markUnknownEnums(schemas []Schema) {
	e := newEnums(schemas)

	for i := range schemas {
		if s := &schemas[i]; !s.IsTypeAlias && e.holdsEnum(s.Name) {
			s.UnknownEnum = e.method(s)
		}
	}
}

// holdsEnum reports whether a value of the Go type typ may hold an enum.
func (e *enums) holdsEnum(typ string) bool {
	if elem, ok := strings.CutPrefix(typ, "*"); ok {
		return e.holdsEnum(elem)
	}

	if elem, ok := strings.CutPrefix(typ, "[]"); ok {
		return e.holdsEnum(elem)
	}

	if key, value, ok := mapTypes(typ); ok {
		return e.holdsEnum(key) || e.holdsEnum(value)
	}

	if _, ok := e.zero[typ]; ok {
		return true
	}

	s := e.byName[typ]
	if s == nil {
		return false
	}

	if holds, ok := e.holds[typ]; ok {
		return holds
	}

	e.holds[typ] = false // a type that refers to itself holds an enum only if another of its members does

	holds := false

	switch s.Kind {
	case SchemaKindAlias:
		holds = e.holdsEnum(s.Type)
	case SchemaKindMap:
		holds = e.holdsEnum(s.MapKey) || e.holdsEnum(s.MapValue)
	case SchemaKindUnion:
		for _, v := range s.UnionVariants {
			holds = holds || e.holdsEnum(v.Type)
		}
	default:
		for _, f := range s.Fields {
			holds = holds || e.holdsEnum(f.Type)
		}
	}

	e.holds[typ] = holds

	return holds
}

// method is the body of the unknownEnum method of the schema s, which holds an enum: an expression of its receiver,
// v, true if any enum v holds has a value the enum does not list.
func (e *enums) method(s *Schema) string {
	var checks []string

	add := func(expr, typ string, optional bool) {
		if c := e.check(expr, typ, optional, 0); c != "" {
			checks = append(checks, c)
		}
	}

	switch s.Kind {
	case SchemaKindAlias:
		if e.byName[s.Type] != nil || e.zero[s.Type] != "" {
			add(convert(s.Type, "v"), s.Type, false) // a new type of another, which has its methods
		} else {
			add("v", s.Type, false)
		}
	case SchemaKindMap:
		add("v", "map["+s.MapKey+"]"+s.MapValue, false)
	case SchemaKindUnion:
		for _, u := range s.UnionVariants {
			if u.Zero != "" {
				add("v."+u.FieldName, u.Type, true)
			} else {
				add("v."+u.FieldName, "*"+u.Type, false)
			}
		}
	case SchemaKindTuple:
		for _, f := range s.Fields {
			add("v."+f.Name, f.Type, false)
		}
	default:
		for _, f := range s.Fields {
			name := f.Name
			if f.Embedded {
				name = strings.TrimPrefix(f.Type, "*")
			}

			add("v."+name, f.Type, !f.Required)
		}
	}

	return strings.Join(checks, " ||\n")
}

// convert is expr, a value of a new type of typ, as one of typ, whose methods it then has.
func convert(typ, expr string) string {
	if strings.HasPrefix(typ, "*") || strings.HasPrefix(typ, "[]") || strings.HasPrefix(typ, "map[") {
		return "(" + typ + ")(" + expr + ")"
	}

	return typ + "(" + expr + ")"
}

// check is an expression true if expr, of the Go type typ, holds a value of an enum the enum does not list, or ""
// if typ holds no enum. Where optional, the zero value of an enum is one left out, and fine. depth names the
// variables of nested loops.
func (e *enums) check(expr, typ string, optional bool, depth int) string {
	if !e.holdsEnum(typ) {
		return ""
	}

	if elem, ok := strings.CutPrefix(typ, "*"); ok {
		inner := expr
		if strings.HasPrefix(elem, "[]") || strings.HasPrefix(elem, "map[") {
			inner = "(*" + expr + ")"
		}

		return expr + " != nil && " + e.check(inner, elem, false, depth)
	}

	x := "x"
	if depth > 0 {
		x += strconv.Itoa(depth + 1)
	}

	if elem, ok := strings.CutPrefix(typ, "[]"); ok {
		return "slices.ContainsFunc(" + expr + ", func(" + x + " " + elem + ") bool { return " +
			e.check(x, elem, false, depth+1) + " })"
	}

	if key, value, ok := mapTypes(typ); ok {
		k, v := "_", "_"

		var checks []string

		if c := e.check("k"+x[1:], key, false, depth+1); c != "" {
			k = "k" + x[1:]

			checks = append(checks, c)
		}

		if c := e.check(x, value, false, depth+1); c != "" {
			v = x

			checks = append(checks, c)
		}

		vars := k + ", " + v
		if v == "_" {
			vars = k
		}

		return "func() bool {\nfor " + vars + " := range " + expr + " {\nif " + strings.Join(checks, " || ") +
			" {\nreturn true\n}\n}\n\nreturn false\n}()"
	}

	if zero, ok := e.zero[typ]; ok {
		if optional {
			return expr + " != " + zero + " && !" + expr + ".Valid()"
		}

		return "!" + expr + ".Valid()"
	}

	if s := e.byName[typ]; s.IsTypeAlias {
		return e.check(expr, s.Type, optional, depth)
	}

	return expr + ".unknownEnum()"
}

// mapTypes is the key and value types of typ, if it is a map type.
func mapTypes(typ string) (key, value string, ok bool) {
	rest, ok := strings.CutPrefix(typ, "map[")
	if !ok {
		return "", "", false
	}

	depth := 1
	for i, r := range rest {
		switch r {
		case '[':
			depth++
		case ']':
			if depth--; depth == 0 {
				return rest[:i], rest[i+1:], true
			}
		}
	}

	return "", "", false
}

// UnknownEnum is, for the decoded value expr of the Go type typ, an expression true if it holds a value of an enum
// the enum does not list, or "" if it can hold none: a response debug mode records.
func (d *Document) UnknownEnum(expr, typ string) string {
	if d.enums == nil {
		d.enums = newEnums(d.Schemas)
	}

	return d.enums.check(expr, typ, false, 0)
}
