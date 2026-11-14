// Package apitest holds what the tests openapi-codegen generates need, as net/http/httptest holds what tests of HTTP
// need: servers that answer as told, the replay of recorded interactions, and the checks of what a client decoded
// and of a type's round trip. It is generated tests', not an API of its own, and changes as the generator needs.
package apitest

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// NewServer is a server that answers every request with status and a JSON content type, but no body, and closes when
// the test ends.
func NewServer(t testing.TB, status int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// BodyServer is the base URL of a server that answers every request with status, contentType and body, and closes
// when the test ends.
func BodyServer(t testing.TB, status int, contentType string, body []byte) *url.URL {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	return u
}

// RoundTripFunc is a function that is an [http.RoundTripper].
type RoundTripFunc func(*http.Request) (*http.Response, error)

// RoundTrip implements [http.RoundTripper].
func (f RoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// unknownMember is a member no type declares.
const unknownMember = `"not_a_member_of_any_type": true`

// WithUnknownMember is v as JSON, encoded as opts say, with a member no type declares added to it.
func WithUnknownMember(t testing.TB, v any, opts json.Options) []byte {
	t.Helper()

	b, err := json.Marshal(v, opts)
	if err != nil {
		t.Fatal(err)
	}

	if string(b) == "{}" {
		return []byte("{" + unknownMember + "}")
	}

	return append([]byte("{"+unknownMember+", "), b[1:]...)
}

// MustDecode decodes data, recorded JSON, into a T as opts say: a value a test builds from a recording it has no
// literal for.
func MustDecode[T any](t testing.TB, data string, opts json.Options) T {
	t.Helper()

	var v T
	if err := json.Unmarshal([]byte(data), &v, opts); err != nil {
		t.Fatalf("decoding %s: %v", data, err)
	}

	return v
}

// RoundTrip encodes in and decodes it strictly into a new value, both as opts say. set must report that the new value
// holds the variant in did, and it must encode as in did.
func RoundTrip[T any](t testing.TB, in T, set func(T) bool, opts json.Options) {
	t.Helper()

	raw, err := json.Marshal(&in, opts)
	if err != nil {
		t.Fatal(err)
	}

	var got T
	if err := json.Unmarshal(raw, &got, opts); err != nil {
		t.Fatalf("%s: %v", raw, err)
	}

	if !set(got) {
		t.Fatalf("%s decoded as another variant: %+v", raw, got)
	}

	again, err := json.Marshal(&got, opts)
	if err != nil {
		t.Fatal(err)
	}

	if string(again) != string(raw) {
		t.Errorf("encoded %s, then %s", raw, again)
	}
}
