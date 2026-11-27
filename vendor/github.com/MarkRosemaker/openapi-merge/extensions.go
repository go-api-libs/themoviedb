package merge

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"

	"github.com/MarkRosemaker/errpath"
	"github.com/MarkRosemaker/openapi"
)

// extensions merges the extensions of b into a: a keeps its own and gains those only b has, in b's order after a's.
// An extension both have must have the same value in both, ignoring white space.
func extensions(a *openapi.Extensions, b openapi.Extensions) error {
	if len(b) == 0 {
		return nil
	}

	if len(*a) == 0 {
		*a = bytes.Clone(b)
		return nil
	}

	aKeys, aVals, err := members(*a)
	if err != nil {
		return err
	}

	bKeys, bVals, err := members(b)
	if err != nil {
		return err
	}

	added := false

	for _, k := range bKeys {
		if av, ok := aVals[k]; ok {
			if !sameJSON(av, bVals[k]) {
				return &errpath.ErrField{Field: k, Err: fmt.Errorf("%s vs. %s", av, bVals[k])}
			}

			continue
		}

		aKeys = append(aKeys, k)
		aVals[k] = bVals[k]
		added = true
	}

	if !added {
		return nil
	}

	var buf bytes.Buffer

	enc := jsontext.NewEncoder(&buf)
	if err := enc.WriteToken(jsontext.BeginObject); err != nil {
		return err
	}

	for _, k := range aKeys {
		if err := enc.WriteToken(jsontext.String(k)); err != nil {
			return err
		}

		if err := enc.WriteValue(aVals[k]); err != nil {
			return err
		}
	}

	if err := enc.WriteToken(jsontext.EndObject); err != nil {
		return err
	}

	*a = bytes.TrimSpace(buf.Bytes())

	return nil
}

// members are the keys of the JSON object v, in order, and their values.
func members(v jsontext.Value) ([]string, map[string]jsontext.Value, error) {
	var keys []string

	vals := map[string]jsontext.Value{}

	dec := jsontext.NewDecoder(bytes.NewReader(v))
	if _, err := dec.ReadToken(); err != nil {
		return nil, nil, err
	}

	for dec.PeekKind() != jsontext.KindEndObject {
		tkn, err := dec.ReadToken()
		if err != nil {
			return nil, nil, err
		}

		k := tkn.String() // before the next read, which may reuse its buffer

		val, err := dec.ReadValue()
		if err != nil {
			return nil, nil, err
		}

		keys = append(keys, k)
		vals[k] = bytes.Clone(val)
	}

	return keys, vals, nil
}

// sameJSON reports whether a and b are the same JSON value.
func sameJSON(a, b jsontext.Value) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return bytes.Equal(a, b)
	}

	xb, errX := json.Marshal(x, json.Deterministic(true))
	yb, errY := json.Marshal(y, json.Deterministic(true))

	return errX == nil && errY == nil && bytes.Equal(xb, yb)
}
