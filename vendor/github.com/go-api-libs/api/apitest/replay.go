package apitest

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"testing"

	"github.com/MarkRosemaker/cassette"
)

// optionalAuthKey marks the context of a call to an operation whose credentials are optional.
type optionalAuthKey struct{}

// OptionalAuth is ctx for a call to an operation whose credentials are optional, whose recording may lack them, see
// [Replay].
func OptionalAuth(ctx context.Context) context.Context {
	return context.WithValue(ctx, optionalAuthKey{}, true)
}

// Replay is a transport that answers calls with the interactions recorded in the file at path. Every call must be the
// next recording, with its headers as recorded but the User-Agent, which names whatever tool made the recording; a
// call marked with [OptionalAuth] may have credentials its recording lacks. A request body may hold a tag its
// recording leaves out, which a client fills in (see jsonSameButTags). The test fails if a recording was never called.
func Replay(t testing.TB, path string) http.RoundTripper {
	t.Helper()

	interactions, err := cassette.InteractionsReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	match := cassette.MatchRequest(
		cassette.CompareHeaders("User-Agent"),
		cassette.OptionalAuthorization(func(req *http.Request) bool { return req.Context().Value(optionalAuthKey{}) != nil }),
	)

	rec := cassette.Replay(interactions, cassette.InOrder(), cassette.WithMatcher(func(req *http.Request, ia cassette.Request) error {
		err := match(req, ia)
		if err == nil || req.Body == nil {
			return err
		}

		// matching drained the body and put it back to be read again
		sent, rerr := io.ReadAll(req.Body)
		if rerr != nil {
			return err
		}

		req.Body = io.NopCloser(bytes.NewReader(sent))

		// the body is the one recorded but for the tags the client fills in: match the rest as it is sent
		var a, b any
		if json.Unmarshal(ia.Body, &a) != nil || json.Unmarshal(sent, &b) != nil || !jsonSameButTags(a, b) {
			return err
		}

		ia.Body = sent

		return match(req, ia)
	}))

	t.Cleanup(func() {
		if unused := rec.Unused(); len(unused) > 0 {
			t.Errorf("%d recorded interactions were never replayed, the first: %s %s", len(unused), unused[0].Request.Method, &unused[0].Request.URL)
		}
	})

	return rec
}

// LastBody passes requests on to Next, and keeps the body of the last response, so that a test can compare what a call
// decoded with what was sent, see [DecodedAsSent].
type LastBody struct {
	Next http.RoundTripper
	Body []byte
}

// RoundTrip implements [http.RoundTripper].
func (l *LastBody) RoundTrip(req *http.Request) (*http.Response, error) {
	rsp, err := l.Next.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	defer rsp.Body.Close()

	if l.Body, err = io.ReadAll(rsp.Body); err != nil {
		return nil, err
	}

	rsp.Body = io.NopCloser(bytes.NewReader(l.Body))

	return rsp, nil
}
