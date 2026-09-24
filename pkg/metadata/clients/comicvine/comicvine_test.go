/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package comicvine_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/comicvine"
)

// strictVolumeServer answers /volume/{guid} only when guid is exactly the
// full "4050-<num>" resource id, and fails the test outright on anything
// else -- including the bare numeric id ("18257") ComicVine's real
// /volume/ endpoint would 404 on. G2-5's fake accepted either shape here,
// which is how "one canonical id can't satisfy both Volume and Issues"
// survived: this is the strict replacement.
func strictVolumeServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/volume/4050-18257/" {
			t.Fatalf("volume endpoint got path %q, want the full \"4050-18257\" guid with ComicVine's trailing slash", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// strictIssuesServer answers /issues/?filter=volume:{num} only when num is
// exactly the bare numeric id, and fails the test outright on anything else
// -- including the prefixed guid ("4050-18257") ComicVine's real filter
// syntax rejects. G2-5's fake accepted either shape here too.
func strictIssuesServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("filter"); got != "volume:18257" {
			t.Fatalf("issues endpoint got filter %q, want the bare numeric \"volume:18257\"", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
}

// failIfCalledServer fails the test if it ever receives a request -- used
// to prove a rejected id (ErrInvalidVolumeID) never reaches the network.
func failIfCalledServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("comicvine: request must not be sent for a rejected id, got %s", r.URL.String())
	}))
}

func TestVolumeMapsComicVineFieldsIntoTheNormalizedModel(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/comicvine/volume_18257.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The trailing slash is the canonical form; without it ComicVine
		// answers 301, one extra round trip per volume.
		require.Equal(t, "/volume/4050-18257/", r.URL.Path)
		require.NotEmpty(t, r.Header.Get("User-Agent"), "ComicVine blocks the Go default User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	v, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	require.NoError(t, err)
	require.Equal(t, "Batman", v.Title)
	require.Equal(t, "DC Comics", v.Publisher)
	require.EqualValues(t, 716, v.IssueCount)
	require.EqualValues(t, 1940, *v.StartYear)
	require.Equal(t, "4050-18257", v.IDs[metadata.KeyComicVine])
}

func TestVolumeMapsA404ToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-99999999"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestVolumeMapsA429WithRetryAfterToRateLimitedError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	var rl *metadata.RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 2*time.Minute, rl.RetryAfter)
}

func TestVolumeRequiresAComicVineID(t *testing.T) {
	c := comicvine.New("test-key", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{})

	require.Error(t, err)
}

// TestSearchVolumesMapsComicVineFieldsIntoSearchHits exercises
// search?resources=volume, documented in docs/research/metadata.md §2.5.
func TestSearchVolumesMapsComicVineFieldsIntoSearchHits(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/comicvine/search_batman.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/search/", r.URL.Path)
		require.Equal(t, "volume", r.URL.Query().Get("resources"))
		require.Equal(t, "batman", r.URL.Query().Get("query"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	hits, err := c.SearchVolumes(context.Background(), "batman")

	require.NoError(t, err)
	require.Len(t, hits, 1)
	require.Equal(t, "Batman", hits[0].Title)
	require.EqualValues(t, 1940, hits[0].Year)
	require.Equal(t, "18257", hits[0].IDs[metadata.KeyComicVine])
	require.Equal(t, "https://comicvine.gamespot.com/a/uploads/original/batman.jpg", hits[0].Poster)
}

// TestIssuesMapsAVolumesIssues exercises /issues/?filter=volume:{id},
// documented in docs/research/metadata.md §2.5.
func TestIssuesMapsAVolumesIssues(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/comicvine/issues_volume_18257.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/issues/", r.URL.Path)
		require.Equal(t, "volume:18257", r.URL.Query().Get("filter"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	issues, err := c.Issues(context.Background(), "18257")

	require.NoError(t, err)
	require.Len(t, issues, 1)
	require.Equal(t, "1", issues[0].Number)
	require.Equal(t, "Batman #1", issues[0].Title)
	require.NotNil(t, issues[0].CoverDate)
	require.True(t, issues[0].CoverDate.Equal(time.Date(1940, 4, 25, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, issues[0].StoreDate)
	require.True(t, issues[0].StoreDate.Equal(time.Date(1940, 3, 1, 0, 0, 0, 0, time.UTC)))
	require.NotNil(t, issues[0].Image)
	require.Equal(t, "https://comicvine.gamespot.com/a/uploads/original/batman-1.jpg", issues[0].Image.URL)
}

// TestOneCanonicalSourceIDDrivesBothVolumeAndIssues is the falsification
// target for G2-5 (defect 1): Comic.spec.sourceID is passed unchanged to
// both Volume (ids[metadata.KeyComicVine]) and Issues (a plain string), and
// against the real ComicVine API those two endpoints want different
// shapes -- /volume/{guid} wants the full "4050-18257" guid,
// /issues/?filter=volume:{num} wants the bare "18257". This proves the
// single canonical, prefixed form (the one a user copies out of a
// ComicVine volume URL, and the one pkg/metadata.Validate's
// comicVinePattern already treats as canonical) drives both calls
// correctly at once, via strictVolumeServer/strictIssuesServer, which fail
// the test outright if either endpoint is asked for the wrong shape.
func TestOneCanonicalSourceIDDrivesBothVolumeAndIssues(t *testing.T) {
	const sourceID = "4050-18257" // Comic.spec.sourceID, unchanged

	volumeBody, err := os.ReadFile("../../../../test/data/metadata/comicvine/volume_18257.json")
	require.NoError(t, err)
	volSrv := strictVolumeServer(t, volumeBody)
	defer volSrv.Close()
	volClient := comicvine.New("test-key", volSrv.Client(), volSrv.URL, metadata.NewLimiter(rate.Inf, 3))

	v, err := volClient.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: sourceID})
	require.NoError(t, err)
	require.Equal(t, "Batman", v.Title)

	issuesBody, err := os.ReadFile("../../../../test/data/metadata/comicvine/issues_volume_18257.json")
	require.NoError(t, err)
	issSrv := strictIssuesServer(t, issuesBody)
	defer issSrv.Close()
	issClient := comicvine.New("test-key", issSrv.Client(), issSrv.URL, metadata.NewLimiter(rate.Inf, 3))

	issues, err := issClient.Issues(context.Background(), sourceID)
	require.NoError(t, err)
	require.Len(t, issues, 1)
}

// TestVolumeAcceptsABareNumericIDAndNormalizesToTheGuid covers the other
// direction: a caller that already has the bare numeric id (as
// ComicIssue.IDs and some historical call sites do) still reaches
// /volume/{guid} with the full guid, and the returned ComicVolume.IDs
// reports the canonical, prefixed form -- not the bare id it was given.
func TestVolumeAcceptsABareNumericIDAndNormalizesToTheGuid(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/comicvine/volume_18257.json")
	require.NoError(t, err)
	srv := strictVolumeServer(t, body)
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	v, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "18257"})

	require.NoError(t, err)
	require.Equal(t, "4050-18257", v.IDs[metadata.KeyComicVine], "output must normalize to the canonical prefixed guid")
}

// TestIssuesAcceptsAPrefixedGuidAndNormalizesToTheBareID covers the other
// direction for Issues: Comic.spec.sourceID's canonical, prefixed form
// still reaches /issues/?filter=volume:{num} with the bare numeric id the
// filter syntax requires.
func TestIssuesAcceptsAPrefixedGuidAndNormalizesToTheBareID(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/comicvine/issues_volume_18257.json")
	require.NoError(t, err)
	srv := strictIssuesServer(t, body)
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	issues, err := c.Issues(context.Background(), "4050-18257")

	require.NoError(t, err)
	require.Len(t, issues, 1)
}

// TestVolumeRejectsAnIssueGuid proves a well-formed but wrong-resource-type
// guid (an issue's "4000-..." id, sent where a volume id belongs) is
// rejected before any request is sent, via ErrInvalidVolumeID, rather than
// forwarded to ComicVine to 404 or -- worse -- silently match the wrong
// resource if ComicVine's server ever ignored the prefix.
func TestVolumeRejectsAnIssueGuid(t *testing.T) {
	srv := failIfCalledServer(t)
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4000-18257"})

	require.ErrorIs(t, err, comicvine.ErrInvalidVolumeID)
}

// TestIssuesRejectsAnIssueGuid is TestVolumeRejectsAnIssueGuid's Issues
// counterpart.
func TestIssuesRejectsAnIssueGuid(t *testing.T) {
	srv := failIfCalledServer(t)
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Issues(context.Background(), "4000-18257")

	require.ErrorIs(t, err, comicvine.ErrInvalidVolumeID)
}

// TestVolumeAndIssuesRejectGarbageVolumeIDs proves neither call forwards a
// malformed id to ComicVine -- empty, non-numeric, or a bare "-" -- rather
// than sending it and letting ComicVine's response (or lack of one) stand
// in for validation.
func TestVolumeAndIssuesRejectGarbageVolumeIDs(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"non-numeric", "batman"},
		{"prefix with no number", "4050-"},
		{"non-numeric suffix", "4050-abc"},
		{"trailing dash", "18257-"},
		{"double dash", "4050--18257"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := failIfCalledServer(t)
			defer srv.Close()
			c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

			_, volErr := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: tt.id})
			require.ErrorIs(t, volErr, comicvine.ErrInvalidVolumeID)

			_, issErr := c.Issues(context.Background(), tt.id)
			require.ErrorIs(t, issErr, comicvine.ErrInvalidVolumeID)
		})
	}
}

func TestVolumeRejectsMalformedResponseBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"truncated JSON", `{"id": 1, "title": "Hea`},
		{"garbage bytes", "not json at all {{{"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

			var v *metadata.ComicVolume
			var err error
			require.NotPanics(t, func() {
				v, err = c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})
			})

			require.Nil(t, v)
			require.Error(t, err)
			require.ErrorIs(t, err, metadata.ErrDecode)
		})
	}
}

// TestAnEnvelopeStatusCodeIsNotAValidResult is the regression for the
// unmapped body-level status_code: ComicVine can answer 200 with a failure
// in its envelope, which used to decode as a valid, empty result list.
func TestAnEnvelopeStatusCodeIsNotAValidResult(t *testing.T) {
	tests := []struct {
		name       string
		httpStatus int
		body       string
		is         error
	}{
		{"invalid api key on a 200", http.StatusOK, `{"error":"Invalid API Key","status_code":100,"results":[]}`, metadata.ErrAuth},
		{"invalid api key on the real 401", http.StatusUnauthorized, `{"error":"Invalid API Key","limit":0,"offset":0,"number_of_page_results":0,"number_of_total_results":0,"status_code":100,"results":[]}`, metadata.ErrAuth},
		{"object not found", http.StatusOK, `{"error":"Object Not Found","status_code":101,"results":[]}`, metadata.ErrNotFound},
		{"subscriber only", http.StatusOK, `{"error":"Subscriber only video is for subscribers only","status_code":105,"results":[]}`, metadata.ErrAuth},
		{"rate limit", http.StatusOK, `{"error":"Rate limit exceeded","status_code":107,"results":[]}`, metadata.ErrRateLimited},
		{"filter error", http.StatusOK, `{"error":"Filter Error","status_code":104,"results":[]}`, nil},
		{"no envelope at all", http.StatusOK, `{"results":[]}`, metadata.ErrDecode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.httpStatus)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

			issues, err := c.Issues(context.Background(), "18257")

			require.Error(t, err, "a non-1 status_code must never read as an empty issue list")
			require.Nil(t, issues)
			if tt.is != nil {
				require.ErrorIs(t, err, tt.is)
			}
		})
	}
}

func TestA420IsRateLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(420)
	}))
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.SearchVolumes(context.Background(), "batman")
	require.ErrorIs(t, err, metadata.ErrRateLimited)
}

func TestVolumeRejectsAnOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":1,"results":{"name":"`))
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(metadata.MaxResponseBytes)))
		_, _ = w.Write([]byte(`"}}`))
	}))
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
	require.NotErrorIs(t, err, metadata.ErrDecode)
}

func TestErrorsNeverCarryTheAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := comicvine.New("secret-key-123", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))
	srv.Close() // every round trip is refused

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-key-123", "net/http's *url.Error message is the whole URL, api_key included")
}

// volumeStatusServer serves the volume fixture that names a last issue,
// and answers that issue with storeDate (or statusCode when non-zero).
func volumeStatusServer(t *testing.T, storeDate string, issueStatus int, issueRequests *int) *httptest.Server {
	t.Helper()
	volume, err := os.ReadFile("../../../../test/data/metadata/comicvine/volume_with_last_issue.json")
	require.NoError(t, err)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/volume/4050-18257/":
			_, _ = w.Write(volume)
		case "/issue/4000-279013/":
			*issueRequests++
			require.Equal(t, "cover_date,store_date", r.URL.Query().Get("field_list"))
			if issueStatus != 0 {
				w.WriteHeader(issueStatus)
				return
			}
			_, _ = w.Write([]byte(`{"error":"OK","status_code":1,"results":{"cover_date":"` + storeDate + `","store_date":"` + storeDate + `"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestVolumeDerivesStatusFromTheLatestIssue is the regression for Volume
// never filling ComicVolume.Status, which left every comic on the gateway's
// daily refresh however long it had been finished.
func TestVolumeDerivesStatusFromTheLatestIssue(t *testing.T) {
	recent := time.Now().UTC().AddDate(0, 0, -20).Format("2006-01-02")
	upcoming := time.Now().UTC().AddDate(0, 0, 14).Format("2006-01-02")
	tests := []struct {
		name      string
		storeDate string
		want      string
	}{
		{"latest issue 20 days ago", recent, "continuing"},
		{"latest issue on sale in two weeks", upcoming, "continuing"},
		{"latest issue years ago", "2011-08-24", "ended"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var issueRequests int
			srv := volumeStatusServer(t, tt.storeDate, 0, &issueRequests)
			defer srv.Close()
			c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

			v, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

			require.NoError(t, err)
			require.Equal(t, tt.want, v.Status)
			require.Equal(t, 1, issueRequests)
		})
	}
}

func TestVolumeStatusIsUnknownWhenTheLatestIssueFetchFails(t *testing.T) {
	var issueRequests int
	srv := volumeStatusServer(t, "", http.StatusInternalServerError, &issueRequests)
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	v, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	require.NoError(t, err, "a derived status must not cost the caller the volume it asked for")
	require.Equal(t, "Batman", v.Title)
	require.Empty(t, v.Status)
}

func TestVolumeWithNoLastIssueMakesNoIssueRequest(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/comicvine/volume_18257.json")
	require.NoError(t, err)
	srv := strictVolumeServer(t, body)
	defer srv.Close()
	c := comicvine.New("test-key", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 3))

	v, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-18257"})

	require.NoError(t, err)
	require.Empty(t, v.Status)
}
