package themoviedb

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cloud.google.com/go/civil"
	"github.com/MarkRosemaker/cassette"
	"github.com/go-api-libs/api"
)

// responseKind is the JSON shape an endpoint answers with.
type responseKind int

const (
	kindObject responseKind = iota
	kindArray
	kindStrings
	kindNone
)

// ok is a body every endpoint of the kind decodes.
func (k responseKind) ok() string {
	if k == kindArray || k == kindStrings {
		return `[]`
	}
	return `{}`
}

// unknownMember is a body only a lenient decoding accepts, empty if the kind has no members.
func (k responseKind) unknownMember() string {
	switch k {
	case kindArray:
		return `[{"unknown_member":true}]`
	case kindStrings:
		return ""
	default:
		return `{"unknown_member":true}`
	}
}

// mismatch is a body of the wrong JSON type, which not even a lenient decoding accepts.
func (k responseKind) mismatch() string {
	if k == kindArray || k == kindStrings {
		return `{}`
	}
	return `[]`
}

type endpoint struct {
	name   string
	method string
	path   string
	query  url.Values
	body   string
	kind   responseKind
	call   func(context.Context, *Client) (any, error)
}

type recordedRequest struct {
	method, path, auth, userAgent, contentType, body string
	query                                            url.Values
}

// newEndpointServer answers every request with body as JSON and records the last request.
func newEndpointServer(t *testing.T, body string) (*url.URL, *recordedRequest) {
	t.Helper()

	rec := &recordedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		*rec = recordedRequest{
			method:      r.Method,
			path:        r.URL.Path,
			query:       r.URL.Query(),
			auth:        r.Header.Get("Authorization"),
			userAgent:   r.Header.Get("User-Agent"),
			contentType: r.Header.Get("Content-Type"),
			body:        string(b),
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)

	u, err := url.Parse(srv.URL + "/3")
	if err != nil {
		t.Fatal(err)
	}

	return u, rec
}

func newEndpointClient(t *testing.T, baseURL *url.URL, opts ...ClientOption) *Client {
	t.Helper()

	c, err := NewClient(append([]ClientOption{WithBaseURL(baseURL), WithBearer("test-token")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}

	return c
}

// wantKept checks that the debug file holds exactly the one call to ep.
func wantKept(t *testing.T, path string, ep endpoint) {
	t.Helper()

	ias, err := cassette.InteractionsReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(ias) != 1 {
		t.Fatalf("got %d kept interactions, want 1", len(ias))
	}

	if ias[0].Request.Method != ep.method || ias[0].Request.URL.Path != ep.path {
		t.Fatalf("kept %s %s, want %s %s", ias[0].Request.Method, ias[0].Request.URL.Path, ep.method, ep.path)
	}
}

func wantDecodingError(t *testing.T, err error) {
	t.Helper()

	if _, ok := errors.AsType[*api.DecodingError](err); !ok {
		t.Fatalf("got %v, want a decoding error", err)
	}
}

func TestEndpoints(t *testing.T) {
	for _, ep := range endpoints {
		t.Run(ep.name, func(t *testing.T) {
			t.Run("request", func(t *testing.T) {
				baseURL, rec := newEndpointServer(t, ep.kind.ok())

				res, err := ep.call(t.Context(), newEndpointClient(t, baseURL))
				if err != nil {
					t.Fatal(err)
				}

				if ep.kind != kindNone && res == nil {
					t.Fatal("got no result")
				}

				if rec.method != ep.method {
					t.Errorf("method: got %s, want %s", rec.method, ep.method)
				}

				if rec.path != ep.path {
					t.Errorf("path: got %s, want %s", rec.path, ep.path)
				}

				if want := cmpQuery(ep.query); !maps.EqualFunc(rec.query, want, func(a, b []string) bool {
					return strings.Join(a, ",") == strings.Join(b, ",")
				}) {
					t.Errorf("query: got %v, want %v", rec.query, want)
				}

				if rec.auth != "Bearer test-token" {
					t.Errorf("Authorization: got %q", rec.auth)
				}

				if rec.userAgent != "tmdb-api" {
					t.Errorf("User-Agent: got %q", rec.userAgent)
				}

				if ep.body != "" {
					if rec.contentType != "application/json" {
						t.Errorf("Content-Type: got %q", rec.contentType)
					}

					if strings.TrimSpace(rec.body) != ep.body {
						t.Errorf("body: got %s, want %s", rec.body, ep.body)
					}
				}
			})

			t.Run("mismatch", func(t *testing.T) {
				baseURL, _ := newEndpointServer(t, ep.kind.mismatch())

				_, err := ep.call(t.Context(), newEndpointClient(t, baseURL))
				wantDecodingError(t, err)
			})

			t.Run("debug mismatch", func(t *testing.T) {
				baseURL, _ := newEndpointServer(t, ep.kind.mismatch())
				debugFile := filepath.Join(t.TempDir(), "interactions.json")

				_, err := ep.call(t.Context(), newEndpointClient(t, baseURL, WithDebugFile(debugFile)))
				wantDecodingError(t, err)
				wantKept(t, debugFile, ep)
			})

			body := ep.kind.unknownMember()
			if body == "" {
				return
			}

			t.Run("strict", func(t *testing.T) {
				baseURL, _ := newEndpointServer(t, body)

				_, err := ep.call(t.Context(), newEndpointClient(t, baseURL))
				wantDecodingError(t, err)
			})

			t.Run("debug lenient", func(t *testing.T) {
				baseURL, _ := newEndpointServer(t, body)
				debugFile := filepath.Join(t.TempDir(), "interactions.json")

				res, err := ep.call(t.Context(), newEndpointClient(t, baseURL, WithDebugFile(debugFile)))
				if err != nil {
					t.Fatal(err)
				}

				if ep.kind != kindNone && res == nil {
					t.Fatal("got no result")
				}

				if ep.kind == kindArray && reflect.ValueOf(res).Len() != 1 {
					t.Fatalf("got %v, want one element", res)
				}

				wantKept(t, debugFile, ep)
			})

			t.Run("debug file unreadable", func(t *testing.T) {
				baseURL, _ := newEndpointServer(t, body)
				debugFile := filepath.Join(t.TempDir(), "interactions.json")
				if err := os.WriteFile(debugFile, []byte("not json"), 0o600); err != nil {
					t.Fatal(err)
				}

				_, err := ep.call(t.Context(), newEndpointClient(t, baseURL, WithDebugFile(debugFile)))
				wantDecodingError(t, err)

				if !strings.Contains(err.Error(), "reading interactions file") {
					t.Fatalf("got %v, want the debug file's error joined", err)
				}
			})
		})
	}
}

// cmpQuery is want, or an empty query for nil, as the server parses no query.
func cmpQuery(want url.Values) url.Values {
	if want == nil {
		return url.Values{}
	}
	return want
}

var endpoints = []endpoint{
	{
		name:   "ValidateKey",
		method: http.MethodGet,
		path:   "/3/authentication",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.ValidateKey(ctx) },
	},
	{
		name:   "AccountDetails",
		method: http.MethodGet,
		path:   "/3/account/101",
		query:  url.Values{"session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountDetails(ctx, 101, &AccountDetailsParams{SessionID: "v-session_id"})
		},
	},
	{
		name:   "AccountAddFavorite",
		method: http.MethodPost,
		path:   "/3/account/102/favorite",
		query:  url.Values{"session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountAddFavorite(ctx, 102, &AccountAddFavoriteParams{SessionID: "v-session_id"}, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "AccountAddToWatchlist",
		method: http.MethodPost,
		path:   "/3/account/103/watchlist",
		query:  url.Values{"session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountAddToWatchlist(ctx, 103, &AccountAddToWatchlistParams{SessionID: "v-session_id"}, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "AccountGetFavorites",
		method: http.MethodGet,
		path:   "/3/account/104/favorite/movies",
		query:  url.Values{"language": {"v-language"}, "page": {"105"}, "session_id": {"v-session_id"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountGetFavorites(ctx, 104, &AccountGetFavoritesParams{Language: "v-language", Page: 105, SessionID: "v-session_id", SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "AccountFavoriteTv",
		method: http.MethodGet,
		path:   "/3/account/106/favorite/tv",
		query:  url.Values{"language": {"v-language"}, "page": {"107"}, "session_id": {"v-session_id"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountFavoriteTv(ctx, 106, &AccountFavoriteTvParams{Language: "v-language", Page: 107, SessionID: "v-session_id", SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "AccountLists",
		method: http.MethodGet,
		path:   "/3/account/108/lists",
		query:  url.Values{"page": {"109"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountLists(ctx, 108, &AccountListsParams{Page: 109, SessionID: "v-session_id"})
		},
	},
	{
		name:   "AccountRatedMovies",
		method: http.MethodGet,
		path:   "/3/account/110/rated/movies",
		query:  url.Values{"language": {"v-language"}, "page": {"111"}, "session_id": {"v-session_id"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountRatedMovies(ctx, 110, &AccountRatedMoviesParams{Language: "v-language", Page: 111, SessionID: "v-session_id", SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "AccountRatedTv",
		method: http.MethodGet,
		path:   "/3/account/112/rated/tv",
		query:  url.Values{"language": {"v-language"}, "page": {"113"}, "session_id": {"v-session_id"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountRatedTv(ctx, 112, &AccountRatedTvParams{Language: "v-language", Page: 113, SessionID: "v-session_id", SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "GetRatedTvEpisodes",
		method: http.MethodGet,
		path:   "/3/account/114/rated/tv/episodes",
		query:  url.Values{"language": {"v-language"}, "page": {"115"}, "session_id": {"v-session_id"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetRatedTvEpisodes(ctx, 114, &GetRatedTvEpisodesParams{Language: "v-language", Page: 115, SessionID: "v-session_id", SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "AccountWatchlistMovies",
		method: http.MethodGet,
		path:   "/3/account/116/watchlist/movies",
		query:  url.Values{"language": {"v-language"}, "page": {"117"}, "session_id": {"v-session_id"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountWatchlistMovies(ctx, 116, &AccountWatchlistMoviesParams{Language: "v-language", Page: 117, SessionID: "v-session_id", SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "AccountWatchlistTv",
		method: http.MethodGet,
		path:   "/3/account/118/watchlist/tv",
		query:  url.Values{"language": {"v-language"}, "page": {"119"}, "session_id": {"v-session_id"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AccountWatchlistTv(ctx, 118, &AccountWatchlistTvParams{Language: "v-language", Page: 119, SessionID: "v-session_id", SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "AuthenticationCreateGuestSession",
		method: http.MethodGet,
		path:   "/3/authentication/guest_session/new",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.AuthenticationCreateGuestSession(ctx) },
	},
	{
		name:   "AuthenticationCreateRequestToken",
		method: http.MethodGet,
		path:   "/3/authentication/token/new",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.AuthenticationCreateRequestToken(ctx) },
	},
	{
		name:   "AuthenticationCreateSession",
		method: http.MethodPost,
		path:   "/3/authentication/session/new",
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AuthenticationCreateSession(ctx, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "AuthenticationCreateSessionFromV4Token",
		method: http.MethodPost,
		path:   "/3/authentication/session/convert/4",
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AuthenticationCreateSessionFromV4Token(ctx, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "AuthenticationCreateSessionFromLogin",
		method: http.MethodPost,
		path:   "/3/authentication/token/validate_with_login",
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AuthenticationCreateSessionFromLogin(ctx, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "AuthenticationDeleteSession",
		method: http.MethodDelete,
		path:   "/3/authentication/session",
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.AuthenticationDeleteSession(ctx, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "CertificationMovieList",
		method: http.MethodGet,
		path:   "/3/certification/movie/list",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.CertificationMovieList(ctx) },
	},
	{
		name:   "CertificationsTvList",
		method: http.MethodGet,
		path:   "/3/certification/tv/list",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.CertificationsTvList(ctx) },
	},
	{
		name:   "ChangesMovieList",
		method: http.MethodGet,
		path:   "/3/movie/changes",
		query:  url.Values{"end_date": {"2024-03-01"}, "page": {"120"}, "start_date": {"2024-03-02"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ChangesMovieList(ctx, &ChangesMovieListParams{EndDate: civil.Date{Year: 2024, Month: 3, Day: 1}, Page: 120, StartDate: civil.Date{Year: 2024, Month: 3, Day: 2}})
		},
	},
	{
		name:   "ChangesPeopleList",
		method: http.MethodGet,
		path:   "/3/person/changes",
		query:  url.Values{"end_date": {"2024-03-03"}, "page": {"121"}, "start_date": {"2024-03-04"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ChangesPeopleList(ctx, &ChangesPeopleListParams{EndDate: civil.Date{Year: 2024, Month: 3, Day: 3}, Page: 121, StartDate: civil.Date{Year: 2024, Month: 3, Day: 4}})
		},
	},
	{
		name:   "ChangesTvList",
		method: http.MethodGet,
		path:   "/3/tv/changes",
		query:  url.Values{"end_date": {"2024-03-05"}, "page": {"122"}, "start_date": {"2024-03-06"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ChangesTvList(ctx, &ChangesTvListParams{EndDate: civil.Date{Year: 2024, Month: 3, Day: 5}, Page: 122, StartDate: civil.Date{Year: 2024, Month: 3, Day: 6}})
		},
	},
	{
		name:   "CollectionDetails",
		method: http.MethodGet,
		path:   "/3/collection/123",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.CollectionDetails(ctx, 123, &CollectionDetailsParams{Language: "v-language"})
		},
	},
	{
		name:   "CollectionImages",
		method: http.MethodGet,
		path:   "/3/collection/124/images",
		query:  url.Values{"include_image_language": {"v-include_image_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.CollectionImages(ctx, 124, &CollectionImagesParams{IncludeImageLanguage: "v-include_image_language", Language: "v-language"})
		},
	},
	{
		name:   "CollectionTranslations",
		method: http.MethodGet,
		path:   "/3/collection/125/translations",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.CollectionTranslations(ctx, 125) },
	},
	{
		name:   "CompanyDetails",
		method: http.MethodGet,
		path:   "/3/company/126",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.CompanyDetails(ctx, 126) },
	},
	{
		name:   "CompanyAlternativeNames",
		method: http.MethodGet,
		path:   "/3/company/127/alternative_names",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.CompanyAlternativeNames(ctx, 127) },
	},
	{
		name:   "CompanyImages",
		method: http.MethodGet,
		path:   "/3/company/128/images",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.CompanyImages(ctx, 128) },
	},
	{
		name:   "ConfigurationDetails",
		method: http.MethodGet,
		path:   "/3/configuration",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.ConfigurationDetails(ctx) },
	},
	{
		name:   "ConfigurationCountries",
		method: http.MethodGet,
		path:   "/3/configuration/countries",
		query:  url.Values{"language": {"v-language"}},
		kind:   kindArray,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ConfigurationCountries(ctx, &ConfigurationCountriesParams{Language: "v-language"})
		},
	},
	{
		name:   "ConfigurationJobs",
		method: http.MethodGet,
		path:   "/3/configuration/jobs",
		kind:   kindArray,
		call:   func(ctx context.Context, c *Client) (any, error) { return c.ConfigurationJobs(ctx) },
	},
	{
		name:   "ConfigurationLanguages",
		method: http.MethodGet,
		path:   "/3/configuration/languages",
		kind:   kindArray,
		call:   func(ctx context.Context, c *Client) (any, error) { return c.ConfigurationLanguages(ctx) },
	},
	{
		name:   "ConfigurationPrimaryTranslations",
		method: http.MethodGet,
		path:   "/3/configuration/primary_translations",
		kind:   kindStrings,
		call:   func(ctx context.Context, c *Client) (any, error) { return c.ConfigurationPrimaryTranslations(ctx) },
	},
	{
		name:   "ConfigurationTimezones",
		method: http.MethodGet,
		path:   "/3/configuration/timezones",
		kind:   kindArray,
		call:   func(ctx context.Context, c *Client) (any, error) { return c.ConfigurationTimezones(ctx) },
	},
	{
		name:   "CreditDetails",
		method: http.MethodGet,
		path:   "/3/credit/p-credit-id",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.CreditDetails(ctx, "p-credit-id", &CreditDetailsParams{Language: "v-language"})
		},
	},
	{
		name:   "DiscoverMovie",
		method: http.MethodGet,
		path:   "/3/discover/movie",
		query:  url.Values{"certification": {"v-certification"}, "certification.gte": {"v-certification.gte"}, "certification.lte": {"v-certification.lte"}, "certification_country": {"v-certification_country"}, "include_adult": {"true"}, "include_video": {"true"}, "language": {"v-language"}, "page": {"129"}, "primary_release_date.gte": {"2024-03-07"}, "primary_release_date.lte": {"2024-03-08"}, "primary_release_year": {"130"}, "region": {"v-region"}, "release_date.gte": {"2024-03-09"}, "release_date.lte": {"2024-03-10"}, "sort_by": {"original_title.asc"}, "vote_average.gte": {"131.5"}, "vote_average.lte": {"132.5"}, "vote_count.gte": {"133.5"}, "vote_count.lte": {"134.5"}, "watch_region": {"v-watch_region"}, "with_cast": {"v-with_cast"}, "with_companies": {"v-with_companies"}, "with_crew": {"v-with_crew"}, "with_genres": {"v-with_genres"}, "with_keywords": {"v-with_keywords"}, "with_origin_country": {"v-with_origin_country"}, "with_original_language": {"v-with_original_language"}, "with_people": {"v-with_people"}, "with_release_type": {"135"}, "with_runtime.gte": {"136"}, "with_runtime.lte": {"137"}, "with_watch_monetization_types": {"v-with_watch_monetization_types"}, "with_watch_providers": {"v-with_watch_providers"}, "without_companies": {"v-without_companies"}, "without_genres": {"v-without_genres"}, "without_keywords": {"v-without_keywords"}, "without_watch_providers": {"v-without_watch_providers"}, "year": {"138"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.DiscoverMovie(ctx, &DiscoverMovieParams{Certification: "v-certification", CertificationGte: "v-certification.gte", CertificationLte: "v-certification.lte", CertificationCountry: "v-certification_country", IncludeAdult: true, IncludeVideo: true, Language: "v-language", Page: 129, PrimaryReleaseYear: 130, PrimaryReleaseDateGte: civil.Date{Year: 2024, Month: 3, Day: 7}, PrimaryReleaseDateLte: civil.Date{Year: 2024, Month: 3, Day: 8}, Region: "v-region", ReleaseDateGte: civil.Date{Year: 2024, Month: 3, Day: 9}, ReleaseDateLte: civil.Date{Year: 2024, Month: 3, Day: 10}, SortBy: SortBy8("original_title.asc"), VoteAverageGte: 131.5, VoteAverageLte: 132.5, VoteCountGte: 133.5, VoteCountLte: 134.5, WatchRegion: "v-watch_region", WithCast: "v-with_cast", WithCompanies: "v-with_companies", WithCrew: "v-with_crew", WithGenres: "v-with_genres", WithKeywords: "v-with_keywords", WithOriginCountry: "v-with_origin_country", WithOriginalLanguage: "v-with_original_language", WithPeople: "v-with_people", WithReleaseType: 135, WithRuntimeGte: 136, WithRuntimeLte: 137, WithWatchMonetizationTypes: "v-with_watch_monetization_types", WithWatchProviders: "v-with_watch_providers", WithoutCompanies: "v-without_companies", WithoutGenres: "v-without_genres", WithoutKeywords: "v-without_keywords", WithoutWatchProviders: "v-without_watch_providers", Year: 138})
		},
	},
	{
		name:   "DiscoverTv",
		method: http.MethodGet,
		path:   "/3/discover/tv",
		query:  url.Values{"air_date.gte": {"2024-03-11"}, "air_date.lte": {"2024-03-12"}, "first_air_date.gte": {"2024-03-13"}, "first_air_date.lte": {"2024-03-14"}, "first_air_date_year": {"139"}, "include_adult": {"true"}, "include_null_first_air_dates": {"true"}, "language": {"v-language"}, "page": {"140"}, "screened_theatrically": {"true"}, "sort_by": {"first_air_date.asc"}, "timezone": {"v-timezone"}, "vote_average.gte": {"141.5"}, "vote_average.lte": {"142.5"}, "vote_count.gte": {"143.5"}, "vote_count.lte": {"144.5"}, "watch_region": {"v-watch_region"}, "with_companies": {"v-with_companies"}, "with_genres": {"v-with_genres"}, "with_keywords": {"v-with_keywords"}, "with_networks": {"145"}, "with_origin_country": {"v-with_origin_country"}, "with_original_language": {"v-with_original_language"}, "with_runtime.gte": {"146"}, "with_runtime.lte": {"147"}, "with_status": {"v-with_status"}, "with_type": {"v-with_type"}, "with_watch_monetization_types": {"v-with_watch_monetization_types"}, "with_watch_providers": {"v-with_watch_providers"}, "without_companies": {"v-without_companies"}, "without_genres": {"v-without_genres"}, "without_keywords": {"v-without_keywords"}, "without_watch_providers": {"v-without_watch_providers"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.DiscoverTv(ctx, &DiscoverTvParams{AirDateGte: civil.Date{Year: 2024, Month: 3, Day: 11}, AirDateLte: civil.Date{Year: 2024, Month: 3, Day: 12}, FirstAirDateYear: 139, FirstAirDateGte: civil.Date{Year: 2024, Month: 3, Day: 13}, FirstAirDateLte: civil.Date{Year: 2024, Month: 3, Day: 14}, IncludeAdult: true, IncludeNullFirstAirDates: true, Language: "v-language", Page: 140, ScreenedTheatrically: true, SortBy: SortBy9("first_air_date.asc"), Timezone: "v-timezone", VoteAverageGte: 141.5, VoteAverageLte: 142.5, VoteCountGte: 143.5, VoteCountLte: 144.5, WatchRegion: "v-watch_region", WithCompanies: "v-with_companies", WithGenres: "v-with_genres", WithKeywords: "v-with_keywords", WithNetworks: 145, WithOriginCountry: "v-with_origin_country", WithOriginalLanguage: "v-with_original_language", WithRuntimeGte: 146, WithRuntimeLte: 147, WithStatus: "v-with_status", WithWatchMonetizationTypes: "v-with_watch_monetization_types", WithWatchProviders: "v-with_watch_providers", WithoutCompanies: "v-without_companies", WithoutGenres: "v-without_genres", WithoutKeywords: "v-without_keywords", WithoutWatchProviders: "v-without_watch_providers", WithType: "v-with_type"})
		},
	},
	{
		name:   "FindByID",
		method: http.MethodGet,
		path:   "/3/find/p-external-id",
		query:  url.Values{"external_source": {"imdb_id"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.FindByID(ctx, "p-external-id", FindByIDParams{ExternalSource: ExternalSource("imdb_id"), Language: "v-language"})
		},
	},
	{
		name:   "GenreMovieList",
		method: http.MethodGet,
		path:   "/3/genre/movie/list",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GenreMovieList(ctx, &GenreMovieListParams{Language: "v-language"})
		},
	},
	{
		name:   "GenreTvList",
		method: http.MethodGet,
		path:   "/3/genre/tv/list",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GenreTvList(ctx, &GenreTvListParams{Language: "v-language"})
		},
	},
	{
		name:   "GuestSessionRatedMovies",
		method: http.MethodGet,
		path:   "/3/guest_session/p-guest-session-id/rated/movies",
		query:  url.Values{"language": {"v-language"}, "page": {"148"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GuestSessionRatedMovies(ctx, "p-guest-session-id", &GuestSessionRatedMoviesParams{Language: "v-language", Page: 148, SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "GuestSessionRatedTv",
		method: http.MethodGet,
		path:   "/3/guest_session/p-guest-session-id/rated/tv",
		query:  url.Values{"language": {"v-language"}, "page": {"149"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GuestSessionRatedTv(ctx, "p-guest-session-id", &GuestSessionRatedTvParams{Language: "v-language", Page: 149, SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "GuestSessionRatedTvEpisodes",
		method: http.MethodGet,
		path:   "/3/guest_session/p-guest-session-id/rated/tv/episodes",
		query:  url.Values{"language": {"v-language"}, "page": {"150"}, "sort_by": {"created_at.asc"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GuestSessionRatedTvEpisodes(ctx, "p-guest-session-id", &GuestSessionRatedTvEpisodesParams{Language: "v-language", Page: 150, SortBy: SortBy("created_at.asc")})
		},
	},
	{
		name:   "KeywordDetails",
		method: http.MethodGet,
		path:   "/3/keyword/151",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.KeywordDetails(ctx, 151) },
	},
	{
		name:   "KeywordMovies",
		method: http.MethodGet,
		path:   "/3/keyword/p-keyword-id/movies",
		query:  url.Values{"include_adult": {"true"}, "language": {"v-language"}, "page": {"152"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.KeywordMovies(ctx, "p-keyword-id", &KeywordMoviesParams{IncludeAdult: true, Language: "v-language", Page: 152})
		},
	},
	{
		name:   "ListAddMovie",
		method: http.MethodPost,
		path:   "/3/list/153/add_item",
		query:  url.Values{"session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListAddMovie(ctx, 153, ListAddMovieParams{SessionID: "v-session_id"}, &ListAddMovie{RawBody: "raw-body"})
		},
	},
	{
		name:   "ListCheckItemStatus",
		method: http.MethodGet,
		path:   "/3/list/154/item_status",
		query:  url.Values{"language": {"v-language"}, "movie_id": {"155"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListCheckItemStatus(ctx, 154, &ListCheckItemStatusParams{Language: "v-language", MovieID: 155})
		},
	},
	{
		name:   "ListClear",
		method: http.MethodPost,
		path:   "/3/list/156/clear",
		query:  url.Values{"confirm": {"true"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListClear(ctx, 156, ListClearParams{SessionID: "v-session_id", Confirm: true})
		},
	},
	{
		name:   "ListCreate",
		method: http.MethodPost,
		path:   "/3/list",
		query:  url.Values{"session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListCreate(ctx, ListCreateParams{SessionID: "v-session_id"}, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "ListDetails",
		method: http.MethodGet,
		path:   "/3/list/157",
		query:  url.Values{"language": {"v-language"}, "page": {"158"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListDetails(ctx, 157, &ListDetailsParams{Language: "v-language", Page: 158})
		},
	},
	{
		name:   "ListDelete",
		method: http.MethodDelete,
		path:   "/3/list/159",
		query:  url.Values{"session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListDelete(ctx, 159, ListDeleteParams{SessionID: "v-session_id"})
		},
	},
	{
		name:   "ListRemoveMovie",
		method: http.MethodPost,
		path:   "/3/list/160/remove_item",
		query:  url.Values{"session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListRemoveMovie(ctx, 160, ListRemoveMovieParams{SessionID: "v-session_id"}, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "GetNowPlaying",
		method: http.MethodGet,
		path:   "/3/movie/now_playing",
		query:  url.Values{"language": {"v-language"}, "page": {"161"}, "region": {"v-region"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetNowPlaying(ctx, &GetNowPlayingParams{Language: "v-language", Page: 161, Region: "v-region"})
		},
	},
	{
		name:   "GetPopular",
		method: http.MethodGet,
		path:   "/3/movie/popular",
		query:  url.Values{"language": {"v-language"}, "page": {"162"}, "region": {"v-region"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetPopular(ctx, &GetPopularParams{Language: "v-language", Page: 162, Region: "v-region"})
		},
	},
	{
		name:   "GetTopRated",
		method: http.MethodGet,
		path:   "/3/movie/top_rated",
		query:  url.Values{"language": {"v-language"}, "page": {"163"}, "region": {"v-region"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetTopRated(ctx, &GetTopRatedParams{Language: "v-language", Page: 163, Region: "v-region"})
		},
	},
	{
		name:   "GetUpcoming",
		method: http.MethodGet,
		path:   "/3/movie/upcoming",
		query:  url.Values{"language": {"v-language"}, "page": {"164"}, "region": {"v-region"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetUpcoming(ctx, &GetUpcomingParams{Language: "v-language", Page: 164, Region: "v-region"})
		},
	},
	{
		name:   "GetMovie",
		method: http.MethodGet,
		path:   "/3/movie/165",
		query:  url.Values{"append_to_response": {"v-append_to_response"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetMovie(ctx, 165, &GetMovieParams{AppendToResponse: "v-append_to_response", Language: "v-language"})
		},
	},
	{
		name:   "MovieAccountStates",
		method: http.MethodGet,
		path:   "/3/movie/166/account_states",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieAccountStates(ctx, 166, &MovieAccountStatesParams{SessionID: "v-session_id", GuestSessionID: "v-guest_session_id"})
		},
	},
	{
		name:   "GetAlternativeTitles",
		method: http.MethodGet,
		path:   "/3/movie/167/alternative_titles",
		query:  url.Values{"country": {"v-country"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetAlternativeTitles(ctx, 167, &GetAlternativeTitlesParams{Country: "v-country"})
		},
	},
	{
		name:   "MovieChanges",
		method: http.MethodGet,
		path:   "/3/movie/168/changes",
		query:  url.Values{"end_date": {"2024-03-15"}, "page": {"169"}, "start_date": {"2024-03-16"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieChanges(ctx, 168, &MovieChangesParams{EndDate: civil.Date{Year: 2024, Month: 3, Day: 15}, Page: 169, StartDate: civil.Date{Year: 2024, Month: 3, Day: 16}})
		},
	},
	{
		name:   "MovieCredits",
		method: http.MethodGet,
		path:   "/3/movie/170/credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieCredits(ctx, 170, &MovieCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "MovieExternalIds",
		method: http.MethodGet,
		path:   "/3/movie/171/external_ids",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.MovieExternalIds(ctx, 171) },
	},
	{
		name:   "GetMovieImages",
		method: http.MethodGet,
		path:   "/3/movie/172/images",
		query:  url.Values{"include_image_language": {"v-include_image_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetMovieImages(ctx, 172, &GetMovieImagesParams{IncludeImageLanguage: "v-include_image_language", Language: "v-language"})
		},
	},
	{
		name:   "MovieKeywords",
		method: http.MethodGet,
		path:   "/3/movie/173/keywords",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.MovieKeywords(ctx, 173) },
	},
	{
		name:   "MovieLatestID",
		method: http.MethodGet,
		path:   "/3/movie/latest",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.MovieLatestID(ctx) },
	},
	{
		name:   "MovieLists",
		method: http.MethodGet,
		path:   "/3/movie/174/lists",
		query:  url.Values{"language": {"v-language"}, "page": {"175"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieLists(ctx, 174, &MovieListsParams{Language: "v-language", Page: 175})
		},
	},
	{
		name:   "MovieRecommendations",
		method: http.MethodGet,
		path:   "/3/movie/176/recommendations",
		query:  url.Values{"language": {"v-language"}, "page": {"177"}},
		kind:   kindNone,
		call: func(ctx context.Context, c *Client) (any, error) {
			return nil, c.MovieRecommendations(ctx, 176, &MovieRecommendationsParams{Language: "v-language", Page: 177})
		},
	},
	{
		name:   "MovieReleaseDates",
		method: http.MethodGet,
		path:   "/3/movie/178/release_dates",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.MovieReleaseDates(ctx, 178) },
	},
	{
		name:   "MovieReviews",
		method: http.MethodGet,
		path:   "/3/movie/179/reviews",
		query:  url.Values{"language": {"v-language"}, "page": {"180"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieReviews(ctx, 179, &MovieReviewsParams{Language: "v-language", Page: 180})
		},
	},
	{
		name:   "MovieSimilar",
		method: http.MethodGet,
		path:   "/3/movie/181/similar",
		query:  url.Values{"language": {"v-language"}, "page": {"182"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieSimilar(ctx, 181, &MovieSimilarParams{Language: "v-language", Page: 182})
		},
	},
	{
		name:   "MovieTranslations",
		method: http.MethodGet,
		path:   "/3/movie/183/translations",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.MovieTranslations(ctx, 183) },
	},
	{
		name:   "MovieVideos",
		method: http.MethodGet,
		path:   "/3/movie/184/videos",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieVideos(ctx, 184, &MovieVideosParams{Language: "v-language"})
		},
	},
	{
		name:   "MovieWatchProviders",
		method: http.MethodGet,
		path:   "/3/movie/185/watch/providers",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.MovieWatchProviders(ctx, 185) },
	},
	{
		name:   "MovieAddRating",
		method: http.MethodPost,
		path:   "/3/movie/186/rating",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieAddRating(ctx, 186, &MovieAddRatingParams{GuestSessionID: "v-guest_session_id", SessionID: "v-session_id"}, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "MovieDeleteRating",
		method: http.MethodDelete,
		path:   "/3/movie/187/rating",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.MovieDeleteRating(ctx, 187, &MovieDeleteRatingParams{GuestSessionID: "v-guest_session_id", SessionID: "v-session_id"})
		},
	},
	{
		name:   "NetworkDetails",
		method: http.MethodGet,
		path:   "/3/network/188",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.NetworkDetails(ctx, 188) },
	},
	{
		name:   "DetailsCopy",
		method: http.MethodGet,
		path:   "/3/network/189/alternative_names",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.DetailsCopy(ctx, 189) },
	},
	{
		name:   "AlternativeNamesCopy",
		method: http.MethodGet,
		path:   "/3/network/190/images",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.AlternativeNamesCopy(ctx, 190) },
	},
	{
		name:   "PersonPopularList",
		method: http.MethodGet,
		path:   "/3/person/popular",
		query:  url.Values{"language": {"v-language"}, "page": {"191"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.PersonPopularList(ctx, &PersonPopularListParams{Language: "v-language", Page: 191})
		},
	},
	{
		name:   "PersonDetails",
		method: http.MethodGet,
		path:   "/3/person/192",
		query:  url.Values{"append_to_response": {"v-append_to_response"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.PersonDetails(ctx, 192, &PersonDetailsParams{AppendToResponse: "v-append_to_response", Language: "v-language"})
		},
	},
	{
		name:   "PersonChanges",
		method: http.MethodGet,
		path:   "/3/person/193/changes",
		query:  url.Values{"end_date": {"2024-03-17"}, "page": {"194"}, "start_date": {"2024-03-18"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.PersonChanges(ctx, 193, &PersonChangesParams{EndDate: civil.Date{Year: 2024, Month: 3, Day: 17}, Page: 194, StartDate: civil.Date{Year: 2024, Month: 3, Day: 18}})
		},
	},
	{
		name:   "PersonCombinedCredits",
		method: http.MethodGet,
		path:   "/3/person/p-person-id/combined_credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.PersonCombinedCredits(ctx, "p-person-id", &PersonCombinedCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "PersonExternalIds",
		method: http.MethodGet,
		path:   "/3/person/195/external_ids",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.PersonExternalIds(ctx, 195) },
	},
	{
		name:   "PersonImages",
		method: http.MethodGet,
		path:   "/3/person/196/images",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.PersonImages(ctx, 196) },
	},
	{
		name:   "PersonLatestID",
		method: http.MethodGet,
		path:   "/3/person/latest",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.PersonLatestID(ctx) },
	},
	{
		name:   "PersonMovieCredits",
		method: http.MethodGet,
		path:   "/3/person/197/movie_credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.PersonMovieCredits(ctx, 197, &PersonMovieCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "PersonTvCredits",
		method: http.MethodGet,
		path:   "/3/person/198/tv_credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.PersonTvCredits(ctx, 198, &PersonTvCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "PersonTaggedImages",
		method: http.MethodGet,
		path:   "/3/person/199/tagged_images",
		query:  url.Values{"page": {"200"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.PersonTaggedImages(ctx, 199, &PersonTaggedImagesParams{Page: 200})
		},
	},
	{
		name:   "Translations",
		method: http.MethodGet,
		path:   "/3/person/201/translations",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.Translations(ctx, 201) },
	},
	{
		name:   "ReviewDetails",
		method: http.MethodGet,
		path:   "/3/review/p-review-id",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.ReviewDetails(ctx, "p-review-id") },
	},
	{
		name:   "SearchCollection",
		method: http.MethodGet,
		path:   "/3/search/collection",
		query:  url.Values{"include_adult": {"true"}, "language": {"v-language"}, "page": {"202"}, "query": {"v-query"}, "region": {"v-region"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.SearchCollection(ctx, SearchCollectionParams{Query: "v-query", IncludeAdult: true, Language: "v-language", Page: 202, Region: "v-region"})
		},
	},
	{
		name:   "SearchCompany",
		method: http.MethodGet,
		path:   "/3/search/company",
		query:  url.Values{"page": {"203"}, "query": {"v-query"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.SearchCompany(ctx, SearchCompanyParams{Query: "v-query", Page: 203})
		},
	},
	{
		name:   "SearchKeyword",
		method: http.MethodGet,
		path:   "/3/search/keyword",
		query:  url.Values{"page": {"204"}, "query": {"v-query"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.SearchKeyword(ctx, SearchKeywordParams{Query: "v-query", Page: 204})
		},
	},
	{
		name:   "SearchMovies",
		method: http.MethodGet,
		path:   "/3/search/movie",
		query:  url.Values{"include_adult": {"true"}, "language": {"v-language"}, "page": {"205"}, "primary_release_year": {"v-primary_release_year"}, "query": {"v-query"}, "region": {"v-region"}, "year": {"206"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.SearchMovies(ctx, SearchMoviesParams{Query: "v-query", IncludeAdult: true, Language: "v-language", PrimaryReleaseYear: "v-primary_release_year", Page: 205, Region: "v-region", Year: 206})
		},
	},
	{
		name:   "SearchMulti",
		method: http.MethodGet,
		path:   "/3/search/multi",
		query:  url.Values{"include_adult": {"true"}, "language": {"v-language"}, "page": {"207"}, "query": {"v-query"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.SearchMulti(ctx, SearchMultiParams{Query: "v-query", IncludeAdult: true, Language: "v-language", Page: 207})
		},
	},
	{
		name:   "SearchPerson",
		method: http.MethodGet,
		path:   "/3/search/person",
		query:  url.Values{"include_adult": {"true"}, "language": {"v-language"}, "page": {"208"}, "query": {"v-query"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.SearchPerson(ctx, SearchPersonParams{Query: "v-query", IncludeAdult: true, Language: "v-language", Page: 208})
		},
	},
	{
		name:   "SearchShows",
		method: http.MethodGet,
		path:   "/3/search/tv",
		query:  url.Values{"first_air_date_year": {"209"}, "include_adult": {"true"}, "language": {"v-language"}, "page": {"210"}, "query": {"v-query"}, "year": {"211"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.SearchShows(ctx, SearchShowsParams{Query: "v-query", FirstAirDateYear: 209, IncludeAdult: true, Language: "v-language", Page: 210, Year: 211})
		},
	},
	{
		name:   "TrendingAll",
		method: http.MethodGet,
		path:   "/3/trending/all/day",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TrendingAll(ctx, TimeWindow("day"), &TrendingAllParams{Language: "v-language"})
		},
	},
	{
		name:   "TrendingMovies",
		method: http.MethodGet,
		path:   "/3/trending/movie/day",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TrendingMovies(ctx, TimeWindow("day"), &TrendingMoviesParams{Language: "v-language"})
		},
	},
	{
		name:   "TrendingPeople",
		method: http.MethodGet,
		path:   "/3/trending/person/day",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TrendingPeople(ctx, TimeWindow("day"), &TrendingPeopleParams{Language: "v-language"})
		},
	},
	{
		name:   "TrendingTv",
		method: http.MethodGet,
		path:   "/3/trending/tv/day",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TrendingTv(ctx, TimeWindow("day"), &TrendingTvParams{Language: "v-language"})
		},
	},
	{
		name:   "TvSeriesAiringTodayList",
		method: http.MethodGet,
		path:   "/3/tv/airing_today",
		query:  url.Values{"language": {"v-language"}, "page": {"212"}, "timezone": {"v-timezone"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesAiringTodayList(ctx, &TvSeriesAiringTodayListParams{Language: "v-language", Page: 212, Timezone: "v-timezone"})
		},
	},
	{
		name:   "TvSeriesOnTheAirList",
		method: http.MethodGet,
		path:   "/3/tv/on_the_air",
		query:  url.Values{"language": {"v-language"}, "page": {"213"}, "timezone": {"v-timezone"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesOnTheAirList(ctx, &TvSeriesOnTheAirListParams{Language: "v-language", Page: 213, Timezone: "v-timezone"})
		},
	},
	{
		name:   "TvSeriesPopularList",
		method: http.MethodGet,
		path:   "/3/tv/popular",
		query:  url.Values{"language": {"v-language"}, "page": {"214"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesPopularList(ctx, &TvSeriesPopularListParams{Language: "v-language", Page: 214})
		},
	},
	{
		name:   "TvSeriesTopRatedList",
		method: http.MethodGet,
		path:   "/3/tv/top_rated",
		query:  url.Values{"language": {"v-language"}, "page": {"215"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesTopRatedList(ctx, &TvSeriesTopRatedListParams{Language: "v-language", Page: 215})
		},
	},
	{
		name:   "GetShow",
		method: http.MethodGet,
		path:   "/3/tv/216",
		query:  url.Values{"append_to_response": {"v-append_to_response"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetShow(ctx, 216, &GetShowParams{AppendToResponse: "v-append_to_response", Language: "v-language"})
		},
	},
	{
		name:   "TvSeriesAccountStates",
		method: http.MethodGet,
		path:   "/3/tv/217/account_states",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesAccountStates(ctx, 217, &TvSeriesAccountStatesParams{SessionID: "v-session_id", GuestSessionID: "v-guest_session_id"})
		},
	},
	{
		name:   "TvSeriesAggregateCredits",
		method: http.MethodGet,
		path:   "/3/tv/218/aggregate_credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesAggregateCredits(ctx, 218, &TvSeriesAggregateCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "TvSeriesAlternativeTitles",
		method: http.MethodGet,
		path:   "/3/tv/219/alternative_titles",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesAlternativeTitles(ctx, 219) },
	},
	{
		name:   "GetShowChanges",
		method: http.MethodGet,
		path:   "/3/tv/220/changes",
		query:  url.Values{"end_date": {"2024-03-19"}, "page": {"221"}, "start_date": {"2024-03-20"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetShowChanges(ctx, 220, &GetShowChangesParams{EndDate: civil.Date{Year: 2024, Month: 3, Day: 19}, Page: 221, StartDate: civil.Date{Year: 2024, Month: 3, Day: 20}})
		},
	},
	{
		name:   "TvSeriesContentRatings",
		method: http.MethodGet,
		path:   "/3/tv/222/content_ratings",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesContentRatings(ctx, 222) },
	},
	{
		name:   "TvSeriesCredits",
		method: http.MethodGet,
		path:   "/3/tv/223/credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesCredits(ctx, 223, &TvSeriesCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "TvSeriesEpisodeGroups",
		method: http.MethodGet,
		path:   "/3/tv/224/episode_groups",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesEpisodeGroups(ctx, 224) },
	},
	{
		name:   "TvSeriesExternalIds",
		method: http.MethodGet,
		path:   "/3/tv/225/external_ids",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesExternalIds(ctx, 225) },
	},
	{
		name:   "TvSeriesImages",
		method: http.MethodGet,
		path:   "/3/tv/226/images",
		query:  url.Values{"include_image_language": {"v-include_image_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesImages(ctx, 226, &TvSeriesImagesParams{IncludeImageLanguage: "v-include_image_language", Language: "v-language"})
		},
	},
	{
		name:   "TvSeriesKeywords",
		method: http.MethodGet,
		path:   "/3/tv/227/keywords",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesKeywords(ctx, 227) },
	},
	{
		name:   "TvSeriesLatestID",
		method: http.MethodGet,
		path:   "/3/tv/latest",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesLatestID(ctx) },
	},
	{
		name:   "ListsCopy",
		method: http.MethodGet,
		path:   "/3/tv/228/lists",
		query:  url.Values{"language": {"v-language"}, "page": {"229"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.ListsCopy(ctx, 228, &ListsCopyParams{Language: "v-language", Page: 229})
		},
	},
	{
		name:   "TvSeriesRecommendations",
		method: http.MethodGet,
		path:   "/3/tv/230/recommendations",
		query:  url.Values{"language": {"v-language"}, "page": {"231"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesRecommendations(ctx, 230, &TvSeriesRecommendationsParams{Language: "v-language", Page: 231})
		},
	},
	{
		name:   "TvSeriesReviews",
		method: http.MethodGet,
		path:   "/3/tv/232/reviews",
		query:  url.Values{"language": {"v-language"}, "page": {"233"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesReviews(ctx, 232, &TvSeriesReviewsParams{Language: "v-language", Page: 233})
		},
	},
	{
		name:   "TvSeriesScreenedTheatrically",
		method: http.MethodGet,
		path:   "/3/tv/234/screened_theatrically",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesScreenedTheatrically(ctx, 234) },
	},
	{
		name:   "TvSeriesSimilar",
		method: http.MethodGet,
		path:   "/3/tv/235/similar",
		query:  url.Values{"language": {"v-language"}, "page": {"236"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesSimilar(ctx, 235, &TvSeriesSimilarParams{Language: "v-language", Page: 236})
		},
	},
	{
		name:   "TvSeriesTranslations",
		method: http.MethodGet,
		path:   "/3/tv/237/translations",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesTranslations(ctx, 237) },
	},
	{
		name:   "TvSeriesVideos",
		method: http.MethodGet,
		path:   "/3/tv/238/videos",
		query:  url.Values{"include_video_language": {"v-include_video_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesVideos(ctx, 238, &TvSeriesVideosParams{IncludeVideoLanguage: "v-include_video_language", Language: "v-language"})
		},
	},
	{
		name:   "TvSeriesWatchProviders",
		method: http.MethodGet,
		path:   "/3/tv/239/watch/providers",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeriesWatchProviders(ctx, 239) },
	},
	{
		name:   "TvSeriesAddRating",
		method: http.MethodPost,
		path:   "/3/tv/240/rating",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesAddRating(ctx, 240, &TvSeriesAddRatingParams{GuestSessionID: "v-guest_session_id", SessionID: "v-session_id"}, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "TvSeriesDeleteRating",
		method: http.MethodDelete,
		path:   "/3/tv/241/rating",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeriesDeleteRating(ctx, 241, &TvSeriesDeleteRatingParams{GuestSessionID: "v-guest_session_id", SessionID: "v-session_id"})
		},
	},
	{
		name:   "GetShowSeason",
		method: http.MethodGet,
		path:   "/3/tv/242/season/243",
		query:  url.Values{"append_to_response": {"v-append_to_response"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetShowSeason(ctx, 242, 243, &GetShowSeasonParams{AppendToResponse: "v-append_to_response", Language: "v-language"})
		},
	},
	{
		name:   "TvSeasonAccountStates",
		method: http.MethodGet,
		path:   "/3/tv/244/season/245/account_states",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeasonAccountStates(ctx, 244, 245, &TvSeasonAccountStatesParams{SessionID: "v-session_id", GuestSessionID: "v-guest_session_id"})
		},
	},
	{
		name:   "TvSeasonAggregateCredits",
		method: http.MethodGet,
		path:   "/3/tv/246/season/247/aggregate_credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeasonAggregateCredits(ctx, 246, 247, &TvSeasonAggregateCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "GetSeasonChanges",
		method: http.MethodGet,
		path:   "/3/tv/season/248/changes",
		query:  url.Values{"end_date": {"2024-03-21"}, "page": {"249"}, "start_date": {"2024-03-22"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetSeasonChanges(ctx, 248, &GetSeasonChangesParams{EndDate: civil.Date{Year: 2024, Month: 3, Day: 21}, Page: 249, StartDate: civil.Date{Year: 2024, Month: 3, Day: 22}})
		},
	},
	{
		name:   "TvSeasonCredits",
		method: http.MethodGet,
		path:   "/3/tv/250/season/251/credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeasonCredits(ctx, 250, 251, &TvSeasonCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "TvSeasonExternalIds",
		method: http.MethodGet,
		path:   "/3/tv/252/season/253/external_ids",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeasonExternalIds(ctx, 252, 253) },
	},
	{
		name:   "TvSeasonImages",
		method: http.MethodGet,
		path:   "/3/tv/254/season/255/images",
		query:  url.Values{"include_image_language": {"v-include_image_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeasonImages(ctx, 254, 255, &TvSeasonImagesParams{IncludeImageLanguage: "v-include_image_language", Language: "v-language"})
		},
	},
	{
		name:   "TvSeasonTranslations",
		method: http.MethodGet,
		path:   "/3/tv/256/season/257/translations",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvSeasonTranslations(ctx, 256, 257) },
	},
	{
		name:   "TvSeasonVideos",
		method: http.MethodGet,
		path:   "/3/tv/258/season/259/videos",
		query:  url.Values{"include_video_language": {"v-include_video_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeasonVideos(ctx, 258, 259, &TvSeasonVideosParams{IncludeVideoLanguage: "v-include_video_language", Language: "v-language"})
		},
	},
	{
		name:   "TvSeasonWatchProviders",
		method: http.MethodGet,
		path:   "/3/tv/260/season/261/watch/providers",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvSeasonWatchProviders(ctx, 260, 261, &TvSeasonWatchProvidersParams{Language: "v-language"})
		},
	},
	{
		name:   "GetEpisodeDetails",
		method: http.MethodGet,
		path:   "/3/tv/262/season/263/episode/264",
		query:  url.Values{"append_to_response": {"v-append_to_response"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.GetEpisodeDetails(ctx, 262, 263, 264, &GetEpisodeDetailsParams{AppendToResponse: "v-append_to_response", Language: "v-language"})
		},
	},
	{
		name:   "TvEpisodeAccountStates",
		method: http.MethodGet,
		path:   "/3/tv/265/season/266/episode/267/account_states",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeAccountStates(ctx, 265, 266, 267, &TvEpisodeAccountStatesParams{SessionID: "v-session_id", GuestSessionID: "v-guest_session_id"})
		},
	},
	{
		name:   "GetEpisodeChanges",
		method: http.MethodGet,
		path:   "/3/tv/episode/268/changes",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.GetEpisodeChanges(ctx, 268) },
	},
	{
		name:   "TvEpisodeCredits",
		method: http.MethodGet,
		path:   "/3/tv/269/season/270/episode/271/credits",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeCredits(ctx, 269, 270, 271, &TvEpisodeCreditsParams{Language: "v-language"})
		},
	},
	{
		name:   "TvEpisodeExternalIds",
		method: http.MethodGet,
		path:   "/3/tv/272/season/273/episode/p-episode-number/external_ids",
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeExternalIds(ctx, 272, 273, "p-episode-number")
		},
	},
	{
		name:   "TvEpisodeImages",
		method: http.MethodGet,
		path:   "/3/tv/274/season/275/episode/276/images",
		query:  url.Values{"include_image_language": {"v-include_image_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeImages(ctx, 274, 275, 276, &TvEpisodeImagesParams{IncludeImageLanguage: "v-include_image_language", Language: "v-language"})
		},
	},
	{
		name:   "TvEpisodeTranslations",
		method: http.MethodGet,
		path:   "/3/tv/277/season/278/episode/279/translations",
		call:   func(ctx context.Context, c *Client) (any, error) { return c.TvEpisodeTranslations(ctx, 277, 278, 279) },
	},
	{
		name:   "TvEpisodeVideos",
		method: http.MethodGet,
		path:   "/3/tv/280/season/281/episode/282/videos",
		query:  url.Values{"include_video_language": {"v-include_video_language"}, "language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeVideos(ctx, 280, 281, 282, &TvEpisodeVideosParams{IncludeVideoLanguage: "v-include_video_language", Language: "v-language"})
		},
	},
	{
		name:   "TvEpisodeAddRating",
		method: http.MethodPost,
		path:   "/3/tv/283/season/284/episode/285/rating",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		body:   `{"RAW_BODY":"raw-body"}`,
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeAddRating(ctx, 283, 284, 285, &TvEpisodeAddRatingParams{GuestSessionID: "v-guest_session_id", SessionID: "v-session_id"}, &ListCreate{RawBody: "raw-body"})
		},
	},
	{
		name:   "TvEpisodeDeleteRating",
		method: http.MethodDelete,
		path:   "/3/tv/286/season/287/episode/288/rating",
		query:  url.Values{"guest_session_id": {"v-guest_session_id"}, "session_id": {"v-session_id"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeDeleteRating(ctx, 286, 287, 288, &TvEpisodeDeleteRatingParams{GuestSessionID: "v-guest_session_id", SessionID: "v-session_id"})
		},
	},
	{
		name:   "TvEpisodeGroupDetails",
		method: http.MethodGet,
		path:   "/3/tv/episode_group/p-tv-episode-group-id",
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.TvEpisodeGroupDetails(ctx, "p-tv-episode-group-id")
		},
	},
	{
		name:   "WatchProvidersAvailableRegions",
		method: http.MethodGet,
		path:   "/3/watch/providers/regions",
		query:  url.Values{"language": {"v-language"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.WatchProvidersAvailableRegions(ctx, &WatchProvidersAvailableRegionsParams{Language: "v-language"})
		},
	},
	{
		name:   "WatchProvidersMovieList",
		method: http.MethodGet,
		path:   "/3/watch/providers/movie",
		query:  url.Values{"language": {"v-language"}, "watch_region": {"v-watch_region"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.WatchProvidersMovieList(ctx, &WatchProvidersMovieListParams{Language: "v-language", WatchRegion: "v-watch_region"})
		},
	},
	{
		name:   "WatchProviderTvList",
		method: http.MethodGet,
		path:   "/3/watch/providers/tv",
		query:  url.Values{"language": {"v-language"}, "watch_region": {"v-watch_region"}},
		call: func(ctx context.Context, c *Client) (any, error) {
			return c.WatchProviderTvList(ctx, &WatchProviderTvListParams{Language: "v-language", WatchRegion: "v-watch_region"})
		},
	},
}
