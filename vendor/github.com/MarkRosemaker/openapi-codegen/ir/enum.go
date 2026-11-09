package ir

import (
	"math/big"
	"slices"
	"strconv"
)

// Enums are the enums a generated test checks the Valid method of: each member is one, and a value of its type that none
// is, if there is such a value, is not.
func (doc Document) Enums() []Schema {
	var enums []Schema

	for _, s := range doc.Schemas {
		if s.Kind == SchemaKindEnum && len(s.EnumValues) > 0 {
			enums = append(enums, s)
		}
	}

	return enums
}

// NotAMember is a Go literal of the enum's underlying type that no member holds, or empty for a boolean, whose two
// values may both be members.
func (s Schema) NotAMember() string {
	switch s.Type {
	case "string":
		lit := "not a member"
		for slices.ContainsFunc(s.EnumValues, func(v EnumValue) bool { return v.Value == lit }) {
			lit += "!"
		}

		return strconv.Quote(lit)
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64":
		// The smallest whole number from 0 up that no member holds: it fits every number type, unsigned and narrow
		// ones too, where one past the highest member can overflow, and members compare exactly, as no float does
		// beyond 2^53.
		members := make([]*big.Rat, 0, len(s.EnumValues))
		for _, v := range s.EnumValues {
			if r, ok := new(big.Rat).SetString(v.Value); ok {
				members = append(members, r)
			}
		}

		for n := int64(0); ; n++ {
			r := big.NewRat(n, 1)
			if !slices.ContainsFunc(members, func(m *big.Rat) bool { return m.Cmp(r) == 0 }) {
				return strconv.FormatInt(n, 10)
			}
		}
	default:
		return ""
	}
}
