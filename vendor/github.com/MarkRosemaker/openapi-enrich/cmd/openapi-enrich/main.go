package main

import (
	"context"
	"crypto/tls"
	"encoding/json/jsontext"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"slices"

	"github.com/MarkRosemaker/cassette"
	"github.com/MarkRosemaker/openapi"
	edit "github.com/MarkRosemaker/openapi-edit"
	enrich "github.com/MarkRosemaker/openapi-enrich"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "openapi-enrich: %v\n", err)
		flag.Usage()
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	var (
		specPath, iaPath, auth string
		trimExamples           int
	)

	flag.StringVar(&specPath, "spec", "api/openapi.json", "path to OpenAPI spec file")
	flag.StringVar(&iaPath, "ia", "api/interactions.json", "path to interactions file")
	flag.StringVar(&auth, "auth", "", "authorization header")
	flag.IntVar(&trimExamples, "trim-examples", 0,
		"cap array length in recorded response bodies to this many representative elements (0 leaves them as recorded)")
	flag.Parse()

	doc, err := openapi.LoadFromFile(specPath)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}

		doc = enrich.NewDocument()
	}

	wasValid := doc.Validate() == nil

	prevIas, err := cassette.InteractionsReadFile(iaPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	// send what has no response yet, and keep what has
	tr := cassette.Replay(prevIas, cassette.RecordMisses(&localhostInsecureTransport{}))

	scaffoldNext := len(prevIas) == 0

	// Call requests that don't have a response yet
	for _, ia := range prevIas {
		if ia.Response.StatusCode > 0 {
			continue // we have a response
		}

		if ia.Request.IsScaffold() {
			scaffoldNext = true
			continue
		}

		req, err := ia.Request.Create(ctx)
		if err != nil {
			return err
		}

		if auth != "" {
			req.Header.Set("Authorization", auth)
		}

		rsp, err := tr.RoundTrip(req)
		if err != nil {
			return err
		}

		rsp.Body.Close()
	}

	// what was recorded before, then what was recorded now; a request answered from a recording is that recording
	ias := slices.DeleteFunc(slices.Clone(prevIas), func(ia *cassette.Interaction) bool { return ia.Response.StatusCode == 0 })
	for _, ia := range tr.Interactions() {
		if !slices.Contains(prevIas, ia) {
			ias = append(ias, ia)
		}
	}

	ias.Mask()

	ias.TrimResponseHeaders()
	ias.TrimBodies(cassette.MaxStringLen)

	if trimExamples > 0 {
		trimResponseBodies(ias, trimExamples)
	}

	recorded := ias

	if scaffoldNext {
		ias = append(ias, &cassette.Interaction{})
	}

	if err := ias.WriteFile(iaPath); err != nil {
		return err
	}

	if err := enrich.Enrich(doc, recorded); err != nil {
		return err
	}

	// Sort responses and components (but not paths to keep the order)
	for _, path := range doc.Paths {
		for _, op := range path.Operations {
			op.Responses.Sort()
		}
	}

	doc.Components.SortMaps()

	if wasValid {
		if err := doc.Validate(); err != nil {
			return fmt.Errorf("produced invalid doc: %w", err)
		}
	}

	if err := doc.WriteToFile(specPath); err != nil {
		return err
	}

	return nil
}

// trimResponseBodies cuts every response body's own arrays down to at most maxItems representative elements, at any
// depth, see [edit.TrimExample]. A body that is empty or not valid JSON is left as it is.
func trimResponseBodies(ias cassette.Interactions, maxItems int) {
	for _, ia := range ias {
		if len(ia.Response.Body) == 0 {
			continue
		}

		if trimmed, err := edit.TrimExample(jsontext.Value(ia.Response.Body), maxItems); err == nil {
			ia.Response.Body = cassette.Body(trimmed)
		}
	}
}

// localhostInsecureTransport is [http.DefaultTransport], but for localhost, whose certificate is self-signed.
type localhostInsecureTransport struct{}

var insecureTransport = &http.Transport{
	// self-signed cert; safe because it's localhost only
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
}

// RoundTrip implements [http.RoundTripper].
func (localhostInsecureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Hostname() == "localhost" {
		return insecureTransport.RoundTrip(req)
	}

	return http.DefaultTransport.RoundTrip(req)
}
