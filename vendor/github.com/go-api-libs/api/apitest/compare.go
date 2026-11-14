package apitest

import (
	"encoding/json/v2"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// DecodedAsSent fails unless out, what a call decoded from body, encodes as opts say to what body holds. A member one side lacks is
// fine where the other holds nothing there (null, false, 0, "", [], an object of nothing but such members, or the zero
// text of a date, a time or a UUID), as an optional value left out is, and so is a tag one side leaves out (see
// jsonInferredTag); members may come in another order. A value may be written another way, as long as it
// is the same: a time as the same instant, a UUID with or without dashes, a number as a string of it.
func DecodedAsSent(t testing.TB, op string, out any, body []byte, opts json.Options) {
	t.Helper()

	encoded, err := json.Marshal(out, opts)
	if err != nil {
		t.Fatalf("%s: encoding what was decoded: %v", op, err)
	}

	var sent, got any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("%s: the recorded body: %v", op, err)
	}

	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("%s: %v", op, err)
	}

	if at, same := jsonSame(sent, got, ""); !same {
		t.Errorf("%s: decoded something else than was sent, at %q:\nsent %s\ngot  %s", op, at, body, encoded)
	}
}

// jsonSame reports whether the JSON values a and b, decoded into any, hold the same, as DecodedAsSent compares them,
// and where they first differ if not.
func jsonSame(a, b any, at string) (string, bool) {
	if jsonEmpty(a) && jsonEmpty(b) || jsonSameInstant(a, b) || jsonSameInstant(b, a) ||
		jsonSameNumber(a, b) || jsonSameNumber(b, a) {
		return "", true
	}

	switch a := a.(type) {
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok {
			return at, false
		}

		names := slices.Sorted(maps.Keys(a))
		for name := range b {
			if _, ok := a[name]; !ok {
				names = append(names, name)
			}
		}

		for _, name := range names {
			if jsonInferredTag(a, b, name) || jsonInferredTag(b, a, name) {
				continue
			}

			if where, same := jsonSame(a[name], b[name], at+"/"+name); !same {
				return where, false
			}
		}

		return "", true
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return at, false
		}

		for i := range a {
			if where, same := jsonSame(a[i], b[i], at+"/"+strconv.Itoa(i)); !same {
				return where, false
			}
		}

		return "", true
	case string:
		b, ok := b.(string)
		if !ok {
			return at, false
		}

		if a == b {
			return "", true
		}

		if ua, ok := jsonUUID(a); ok {
			if ub, ok := jsonUUID(b); ok {
				return at, ua == ub
			}
		}

		ta, errA := time.Parse(time.RFC3339Nano, a)
		tb, errB := time.Parse(time.RFC3339Nano, b)

		return at, errA == nil && errB == nil && ta.Equal(tb)
	default:
		return at, a == b
	}
}

// jsonSameInstant reports whether a is a Unix time, in seconds or milliseconds, and b a date-time of the same instant,
// as a type that takes either writes one as the other.
func jsonSameInstant(a, b any) bool {
	n, ok := a.(float64)
	if !ok {
		return false
	}

	s, ok := b.(string)
	if !ok {
		return false
	}

	ts, err := time.Parse(time.RFC3339Nano, s)

	return err == nil && (float64(ts.Unix()) == n || float64(ts.UnixMilli()) == n)
}

// jsonSameNumber reports whether a is a number and b a string of the same number, as a string type writes one.
func jsonSameNumber(a, b any) bool {
	n, ok := a.(float64)
	if !ok {
		return false
	}

	s, ok := b.(string)
	if !ok {
		return false
	}

	f, err := strconv.ParseFloat(s, 64)

	return err == nil && f == n
}

// jsonUUID is s, a UUID with or without dashes, as its 32 hex digits in lower case.
func jsonUUID(s string) (string, bool) {
	hex := strings.ToLower(strings.ReplaceAll(s, "-", ""))
	if len(hex) != 32 || len(s) != 32 && len(s) != 36 || strings.Trim(hex, "0123456789abcdef") != "" {
		return "", false
	}

	return hex, true
}

// jsonEmpty reports whether v, a JSON value decoded into any, holds nothing: null, false, 0, "", [], an object whose
// members hold nothing, or the text a zero date, time or UUID is written as, which is what an empty one decodes into:
// null read into a struct is written back as its members, empty.
func jsonEmpty(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case bool:
		return !v
	case float64:
		return v == 0
	case string:
		return v == "" || v == "0000-00-00" || v == "0001-01-01T00:00:00Z" || v == "00000000-0000-0000-0000-000000000000"
	case []any:
		return len(v) == 0
	case map[string]any:
		for _, m := range v {
			if !jsonEmpty(m) {
				return false
			}
		}

		return true
	default:
		return false
	}
}

// jsonInferredTag reports whether the object a lacks the member name, which b holds as a tag: the name of a member both
// hold. A tagged object may leave its tag out, and decoding it infers the tag from the member that is set, which
// encoding then writes.
func jsonInferredTag(a, b map[string]any, name string) bool {
	if _, ok := a[name]; ok {
		return false
	}

	tag, ok := b[name].(string)
	if !ok {
		return false
	}

	_, inA := a[tag]
	_, inB := b[tag]

	return inA && inB
}

// jsonSameButTags reports whether the JSON values a and b, decoded into any, are the same, but for tags either leaves
// out (see jsonInferredTag): the request a call sends for one that was recorded without them.
func jsonSameButTags(a, b any) bool {
	switch a := a.(type) {
	case map[string]any:
		b, ok := b.(map[string]any)
		if !ok {
			return false
		}

		for name := range a {
			if _, ok := b[name]; !ok && !jsonInferredTag(b, a, name) {
				return false
			}
		}

		for name, vb := range b {
			va, ok := a[name]
			if !ok && !jsonInferredTag(a, b, name) || ok && !jsonSameButTags(va, vb) {
				return false
			}
		}

		return true
	case []any:
		b, ok := b.([]any)
		if !ok || len(a) != len(b) {
			return false
		}

		for i := range a {
			if !jsonSameButTags(a[i], b[i]) {
				return false
			}
		}

		return true
	default:
		return a == b
	}
}
