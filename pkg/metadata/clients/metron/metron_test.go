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

package metron_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
	"github.com/mediactl/clustarr/pkg/metadata/clients/metron"
)

// The fixtures follow Metron's serializers (Metron-Project/metron
// api/v1_0/serializers/series.py) and mokkari's test data for series 1,
// "Death of the Inhumans"; the API needs an account, so no live response
// was captured.
const fixtures = "../../../../testdata/metadata/metron/"

// serve routes "path?rawquery" to a fixture; the "next" links inside the
// fixtures point at metron.cloud and must be rewritten onto this server by
// the client, never followed.
func serve(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Path
		if r.URL.RawQuery != "" {
			key += "?" + r.URL.RawQuery
		}
		seen = append(seen, key)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			b, _ := os.ReadFile(fixtures + "unauthorized.json")
			_, _ = w.Write(b)
			return
		}
		name, ok := routes[key]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newClient(t *testing.T, srv *httptest.Server, tok string) *metron.Client {
	t.Helper()
	c, err := metron.New(metron.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Token: tok})
	require.NoError(t, err)
	return c
}

func i32(v int32) *int32 { return &v }

func TestSearchVolumesCarriesTheCrosswalk(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/series/?name=death": "series_list_death.json"})
	c := newClient(t, srv, "tok")

	hits, err := c.SearchVolumes(context.Background(), "death")

	require.NoError(t, err)
	require.Equal(t, []string{"/series/?name=death"}, *seen)
	require.Equal(t, []metadata.SearchHit{
		{IDs: metadata.ExternalIDs{extid.KeyMetron: "1", metadata.KeyComicVine: "4050-111265"}, Title: "Death of the Inhumans (2018)", Year: 2018},
		{IDs: metadata.ExternalIDs{extid.KeyMetron: "2311"}, Title: "Absolute Carnage (2019)", Year: 2019},
	}, hits)
}

func TestVolumeByComicVineCrosswalksThenFetches(t *testing.T) {
	srv, seen := serve(t, map[string]string{
		"/series/?cv_id=111265": "series_list_cv_111265.json",
		"/series/1/":            "series_1.json",
	})
	c := newClient(t, srv, "tok")

	v, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "4050-111265"})

	require.NoError(t, err)
	require.Equal(t, []string{"/series/?cv_id=111265", "/series/1/"}, *seen, "the guid is sent as its bare number")
	require.Equal(t, metadata.ExternalIDs{extid.KeyMetron: "1", metadata.KeyComicVine: "4050-111265", extid.KeyGCD: "1234"}, v.IDs)
	require.Equal(t, "Death of the Inhumans", v.Title)
	require.Equal(t, "Marvel", v.Publisher)
	require.Equal(t, i32(2018), v.StartYear)
	require.Equal(t, i32(2018), v.EndYear)
	require.EqualValues(t, 5, v.IssueCount)
	require.Equal(t, "completed", v.Status)
	require.Equal(t, "en", v.OriginalLanguage)
	require.Equal(t, []string{"Super-Hero"}, v.Genres)
	require.Equal(t, []metadata.AltTitle{{Title: "La Mort des Inhumains"}}, v.AltTitles)
}

func TestVolumeByMetronIDSkipsTheCrosswalk(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/series/1/": "series_1.json"})
	c := newClient(t, srv, "tok")

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{extid.KeyMetron: "1", metadata.KeyComicVine: "4050-111265"})

	require.NoError(t, err)
	require.Equal(t, []string{"/series/1/"}, *seen)
}

func TestACrosswalkThatFindsNothingOrTooMuchNeverGuesses(t *testing.T) {
	srv, _ := serve(t, map[string]string{
		"/series/?cv_id=1":      "series_list_empty.json",
		"/series/?cv_id=111265": "series_list_cv_ambiguous.json",
	})
	c := newClient(t, srv, "tok")

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{metadata.KeyComicVine: "1"})
	require.ErrorIs(t, err, metadata.ErrNotFound)

	_, err = c.Issues(context.Background(), "4050-111265")
	require.ErrorIs(t, err, metron.ErrAmbiguous)
}

func TestIssuesReadsAComicVineIDAndFollowsPagesOnItsOwnHost(t *testing.T) {
	srv, seen := serve(t, map[string]string{
		"/series/?cv_id=111265":        "series_list_cv_111265.json",
		"/series/1/issue_list/":        "issue_list_1_page1.json",
		"/series/1/issue_list/?page=2": "issue_list_1_page2.json",
	})
	c := newClient(t, srv, "tok")

	issues, err := c.Issues(context.Background(), "111265")

	require.NoError(t, err)
	require.Equal(t, []string{"/series/?cv_id=111265", "/series/1/issue_list/", "/series/1/issue_list/?page=2"}, *seen,
		"the next link names metron.cloud; only its query is reused")
	require.Len(t, issues, 3)
	cover := time.Date(2018, time.September, 1, 0, 0, 0, 0, time.UTC)
	store := time.Date(2018, time.July, 4, 0, 0, 0, 0, time.UTC)
	require.Equal(t, metadata.ComicIssue{
		IDs:       metadata.ExternalIDs{extid.KeyMetron: "3615"},
		Number:    "1",
		CoverDate: &cover,
		StoreDate: &store,
		Image:     &metadata.Image{Type: metadata.ImageTypeThumb, URL: "https://static.metron.cloud/media/issue/2018/11/11/6497376-01.jpg"},
	}, issues[0])
	require.Nil(t, issues[1].StoreDate)
	require.Nil(t, issues[1].Image)
	require.Equal(t, "2.5", issues[2].Number)
}

func TestIssuesRefusesAnIssueGuid(t *testing.T) {
	srv, seen := serve(t, nil)
	c := newClient(t, srv, "tok")

	_, err := c.Issues(context.Background(), "4000-6497376")

	require.ErrorIs(t, err, metron.ErrInvalidID)
	require.Empty(t, *seen)
}

func TestResolveAddsTheMetronAndGCDIDs(t *testing.T) {
	srv, _ := serve(t, map[string]string{"/series/?cv_id=111265": "series_list_cv_111265.json"})
	c := newClient(t, srv, "tok")

	ids, err := c.Resolve(context.Background(), commonv1.MediaKindComic, metadata.ExternalIDs{metadata.KeyComicVine: "4050-111265"})
	require.NoError(t, err)
	require.Equal(t, "1", ids[extid.KeyMetron])

	_, err = c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "603"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

func TestUnusableIDsAreUnsupportedWithoutARequest(t *testing.T) {
	srv, seen := serve(t, nil)
	c := newClient(t, srv, "tok")

	_, err := c.Volume(context.Background(), metadata.ExternalIDs{extid.KeyMangaDex: "801513ba-a712-498c-8f57-cae55b38cc92"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	require.Empty(t, *seen)
}

func TestARejectedTokenIsErrAuth(t *testing.T) {
	srv, _ := serve(t, nil)
	require.ErrorIs(t, newClient(t, srv, "wrong").Ping(context.Background()), metadata.ErrAuth)
}

func TestPingIsOneFilteredList(t *testing.T) {
	srv, seen := serve(t, map[string]string{"/series/?cv_id=796": "series_list_empty.json"})
	require.NoError(t, newClient(t, srv, "Bearer tok").Ping(context.Background()))
	require.Len(t, *seen, 1)
}

func TestNewRequiresAToken(t *testing.T) {
	_, err := metron.New(metron.Config{})
	require.ErrorIs(t, err, metron.ErrNoToken)
}
