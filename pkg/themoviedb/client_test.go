package themoviedb

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cloud.google.com/go/civil"
	"github.com/MarkRosemaker/cassette"
	"github.com/go-api-libs/api"
)

func TestNewClient(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		t.Setenv("TMDB_API_TOKEN", "")

		if _, err := NewClient(); err == nil {
			t.Fatal("expected error")
		}
	})

	for _, tc := range []struct {
		name, env string
		opts      []ClientOption
		want      string
	}{
		{name: "token from the environment", env: "env-token", want: "Bearer env-token"},
		{name: "option overrides the environment", env: "env-token", opts: []ClientOption{WithBearer("opt-token")}, want: "Bearer opt-token"},
		{name: "prefix is not doubled", opts: []ClientOption{WithBearer("Bearer opt-token")}, want: "Bearer opt-token"},
		{name: "empty option keeps the environment", env: "env-token", opts: []ClientOption{WithBearer("")}, want: "Bearer env-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMDB_API_TOKEN", tc.env)

			baseURL, rec := newEndpointServer(t, `{}`)

			c, err := NewClient(append([]ClientOption{WithBaseURL(baseURL)}, tc.opts...)...)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := c.ValidateKey(t.Context()); err != nil {
				t.Fatal(err)
			}

			if rec.auth != tc.want {
				t.Fatalf("Authorization: got %q, want %q", rec.auth, tc.want)
			}
		})
	}
}

func TestWithBaseURL(t *testing.T) {
	u := &url.URL{Scheme: "http", Host: "example.com", Path: "api/3"}
	WithBaseURL(u)

	if u.Path != "/api/3" {
		t.Fatalf("got %q, want %q", u.Path, "/api/3")
	}
}

func TestClient_WithResult(t *testing.T) {
	baseURL, rec := newEndpointServer(t, `{"page":2,"results":[],"total_pages":9}`)
	c := newEndpointClient(t, baseURL)

	type page struct {
		Page int `json:"page"`
	}

	res, err := c.SearchMoviesWithResult[page](t.Context(), SearchMoviesParams{Query: "Alien"})
	if err != nil {
		t.Fatal(err)
	}

	if res.Page != 2 {
		t.Fatalf("got page %d, want 2", res.Page)
	}

	if rec.path != "/3/search/movie" || rec.query.Get("query") != "Alien" {
		t.Fatalf("got %s?%s", rec.path, rec.query.Encode())
	}
}

func TestClient_Recorded(t *testing.T) {
	ctx := t.Context()
	t.Setenv("TMDB_API_TOKEN", "**************************************************")

	interactions, err := cassette.InteractionsReadFile("../../api/interactions.json")
	if err != nil {
		t.Fatal(err)
	}

	c, err := NewClient(WithHTTPClient(&http.Client{Transport: cassette.Replay(interactions)}))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("SearchMovies", func(t *testing.T) {
		res, err := c.SearchMovies(ctx, SearchMoviesParams{Query: "The Return of the King", Year: 2003})
		if err != nil {
			t.Fatal(err)
		}

		if res.Page != 1 || len(res.Results) == 0 {
			t.Fatalf("got page %d with %d results", res.Page, len(res.Results))
		}

		if m := res.Results[0]; m.ID != 122 || m.Title != "The Lord of the Rings: The Return of the King" {
			t.Fatalf("got %d %q", m.ID, m.Title)
		}
	})

	t.Run("GetMovie", func(t *testing.T) {
		res, err := c.GetMovie(ctx, 55931, &GetMovieParams{})
		if err != nil {
			t.Fatal(err)
		}

		if res.ID != 55931 || res.ImdbID != "tt0328832" || res.Budget != 5000000 {
			t.Fatalf("got %d %q %d", res.ID, res.ImdbID, res.Budget)
		}

		if res.Homepage.Host != "www.warnerbros.com" {
			t.Fatalf("got homepage %v", &res.Homepage)
		}
	})

	t.Run("SearchShows", func(t *testing.T) {
		res, err := c.SearchShows(ctx, SearchShowsParams{Query: "The Crown"})
		if err != nil {
			t.Fatal(err)
		}

		if len(res.Results) == 0 || res.Results[0].ID != 65494 || res.Results[0].Name != "The Crown" {
			t.Fatalf("got %+v", res.Results)
		}
	})

	t.Run("GetShowSeason", func(t *testing.T) {
		res, err := c.GetShowSeason(ctx, 65494, 1, &GetShowSeasonParams{})
		if err != nil {
			t.Fatal(err)
		}

		if want := (civil.Date{Year: 2016, Month: 11, Day: 4}); res.AirDate != want {
			t.Fatalf("got air date %v, want %v", res.AirDate, want)
		}

		if len(res.Episodes) == 0 || res.Episodes[0].Name != "Wolferton Splash" || res.Episodes[0].EpisodeNumber != 1 {
			t.Fatalf("got %d episodes", len(res.Episodes))
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := c.GetShowSeason(ctx, 6138, 2, &GetShowSeasonParams{})

		apiErr, ok := errors.AsType[*Error](err)
		if !ok {
			t.Fatalf("got %T, want *Error", err)
		}

		if apiErr.StatusCode != 34 || apiErr.Success {
			t.Fatalf("got %+v", apiErr)
		}

		if want := "34 The resource you requested could not be found."; apiErr.Error() != want {
			t.Fatalf("got %q, want %q", apiErr.Error(), want)
		}
	})
}

func TestClient_ErrorResponses(t *testing.T) {
	newServer := func(t *testing.T, status int, body string) *Client {
		t.Helper()

		srv := newTestServerWithBody(t, status, body)

		baseURL, err := url.Parse(srv)
		if err != nil {
			t.Fatal(err)
		}

		return newEndpointClient(t, baseURL)
	}

	t.Run("unauthorized", func(t *testing.T) {
		c := newServer(t, http.StatusUnauthorized, `{"success":false,"status_code":7,"status_message":"Invalid API key: You must be granted a valid key."}`)

		_, err := c.ValidateKey(t.Context())

		apiErr, ok := errors.AsType[*Error](err)
		if !ok {
			t.Fatalf("got %T, want *Error", err)
		}

		if apiErr.StatusCode != 7 {
			t.Fatalf("got %+v", apiErr)
		}

		if wrapped, ok := errors.AsType[*api.Error](err); !ok || !wrapped.IsCustom || wrapped.Response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("got %v, want a custom api.Error with status 401", err)
		}
	})

	t.Run("unauthorized undecodable", func(t *testing.T) {
		c := newServer(t, http.StatusUnauthorized, `{"status_code":"seven"}`)

		_, err := c.ValidateKey(t.Context())
		wantDecodingError(t, err)
	})

	t.Run("not found with unknown member in debug mode", func(t *testing.T) {
		debugFile := filepath.Join(t.TempDir(), "interactions.json")

		baseURL, err := url.Parse(newTestServerWithBody(t, http.StatusNotFound, `{"success":false,"status_code":34,"status_message":"gone","extra":1}`))
		if err != nil {
			t.Fatal(err)
		}

		c := newEndpointClient(t, baseURL, WithDebugFile(debugFile))

		_, err = c.GetShowSeason(t.Context(), 1, 2, nil)
		if apiErr, ok := errors.AsType[*Error](err); !ok || apiErr.StatusCode != 34 {
			t.Fatalf("got %v, want *Error with status code 34", err)
		}

		if _, err := os.Stat(debugFile); err != nil {
			t.Fatalf("want the call kept: %v", err)
		}
	})
}

func newTestServerWithBody(t *testing.T, status int, body string) string {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	return srv.URL + "/3"
}

func TestClient_Debug(t *testing.T) {
	t.Run("decodable response is not kept", func(t *testing.T) {
		baseURL, _ := newEndpointServer(t, `{"success":true}`)
		debugFile := filepath.Join(t.TempDir(), "interactions.json")

		if _, err := newEndpointClient(t, baseURL, WithDebugFile(debugFile)).ValidateKey(t.Context()); err != nil {
			t.Fatal(err)
		}

		if _, err := os.Stat(debugFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("want no debug file, got %v", err)
		}
	})

	t.Run("repeated failure is kept once", func(t *testing.T) {
		baseURL, _ := newEndpointServer(t, `{"success":true,"unknown_member":1}`)
		debugFile := filepath.Join(t.TempDir(), "interactions.json")
		c := newEndpointClient(t, baseURL, WithDebugFile(debugFile))

		for range 3 {
			res, err := c.ValidateKey(t.Context())
			if err != nil {
				t.Fatal(err)
			}

			if !res.Success {
				t.Fatalf("got %+v, want the known member decoded", res)
			}
		}

		wantKept(t, debugFile, endpoint{method: http.MethodGet, path: "/3/authentication"})
	})

	t.Run("token is masked", func(t *testing.T) {
		baseURL, _ := newEndpointServer(t, `{"unknown_member":1}`)
		debugFile := filepath.Join(t.TempDir(), "interactions.json")

		if _, err := newEndpointClient(t, baseURL, WithDebugFile(debugFile)).ValidateKey(t.Context()); err != nil {
			t.Fatal(err)
		}

		b, err := os.ReadFile(debugFile)
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(string(b), "test-token") {
			t.Fatalf("debug file holds the bearer token: %s", b)
		}
	})

	t.Run("unknown status code is kept", func(t *testing.T) {
		debugFile := filepath.Join(t.TempDir(), "interactions.json")

		baseURL, err := url.Parse(newTestServerWithBody(t, http.StatusTeapot, `{}`))
		if err != nil {
			t.Fatal(err)
		}

		_, err = newEndpointClient(t, baseURL, WithDebugFile(debugFile)).ValidateKey(t.Context())
		if !errors.Is(err, api.ErrUnknownStatusCode) {
			t.Fatalf("got %v, want %v", err, api.ErrUnknownStatusCode)
		}

		wantKept(t, debugFile, endpoint{method: http.MethodGet, path: "/3/authentication"})
	})

	t.Run("default debug file", func(t *testing.T) {
		t.Chdir(t.TempDir())
		baseURL, _ := newEndpointServer(t, `{"unknown_member":1}`)

		c, err := NewClient(WithBaseURL(baseURL), WithBearer("test-token"), WithDebug)
		if err != nil {
			t.Fatal(err)
		}

		if _, err := c.ValidateKey(t.Context()); err != nil {
			t.Fatal(err)
		}

		wantKept(t, filepath.Join("api", "interactions.json"), endpoint{method: http.MethodGet, path: "/3/authentication"})
	})

	t.Run("transport error", func(t *testing.T) {
		c := newEndpointClient(t, &url.URL{Scheme: "http", Host: "example.com"},
			WithDebugFile(filepath.Join(t.TempDir(), "interactions.json")),
			WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, io.ErrUnexpectedEOF
			})}))

		if _, err := c.ValidateKey(t.Context()); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("got %v, want %v", err, io.ErrUnexpectedEOF)
		}
	})
}

func TestClient_GetImage(t *testing.T) {
	var got *http.Request

	c := newEndpointClient(t, &url.URL{Scheme: "http", Host: "example.com"},
		WithHTTPClient(&http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			got = r
			status := http.StatusOK
			if strings.HasSuffix(r.URL.Path, "missing.jpg") {
				status = http.StatusNotFound
			}

			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"Content-Type": {"image/jpeg"}},
				Body:       io.NopCloser(strings.NewReader("jpeg bytes")),
				Request:    r,
			}, nil
		})}))

	t.Run("found", func(t *testing.T) {
		rc, err := c.GetImage(t.Context(), "/7a4WHNllmX5ZGR0MfYJDPhV69eM.jpg")
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()

		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}

		if string(b) != "jpeg bytes" {
			t.Fatalf("got %q", b)
		}

		if want := "https://image.tmdb.org/t/p/original/7a4WHNllmX5ZGR0MfYJDPhV69eM.jpg"; got.URL.String() != want {
			t.Fatalf("got %s, want %s", got.URL, want)
		}

		if got.Method != http.MethodGet || got.Header.Get("Authorization") != "Bearer test-token" || got.Header.Get("User-Agent") != "tmdb-api" {
			t.Fatalf("got %s with headers %v", got.Method, got.Header)
		}
	})

	t.Run("not found", func(t *testing.T) {
		if _, err := c.GetImage(t.Context(), "missing.jpg"); !errors.Is(err, api.ErrUnknownStatusCode) {
			t.Fatalf("got %v, want %v", err, api.ErrUnknownStatusCode)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		c := newEndpointClient(t, &url.URL{Scheme: "http", Host: "example.com"},
			WithHTTPClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, io.ErrUnexpectedEOF
			})}))

		if _, err := c.GetImage(t.Context(), "a.jpg"); !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("got %v, want %v", err, io.ErrUnexpectedEOF)
		}
	})
}
