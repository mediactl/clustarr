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

package musicbrainz_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/musicbrainz"
)

func TestArtistSendsTheMandatoryContactUserAgentAndMapsFields(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/artist_radiohead.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Contains(t, r.Header.Get("User-Agent"), "Clustarr/", "MusicBrainz requires a contact User-Agent or it throttles to the shared anonymous bucket")
		require.Contains(t, r.URL.Query().Get("inc"), "ratings")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	a, err := c.Artist(context.Background(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")

	require.NoError(t, err)
	require.Equal(t, "Radiohead", a.Name)
	require.Equal(t, "Group", a.Type)
	require.Equal(t, "GB", a.Country)
	require.EqualValues(t, 900, a.Ratings["mb"].ValueCentis, "MusicBrainz 4.5/5 normalized to a /10 scale, ×100")
	require.Contains(t, a.Links, metadata.Link{Type: "wikidata", URL: "https://www.wikidata.org/wiki/Q6033"})
}

func TestSearchArtistsIsUnsupported(t *testing.T) {
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", http.DefaultClient, "", metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.SearchArtists(context.Background(), "radiohead")

	require.ErrorIs(t, err, metadata.ErrUnsupported)
}

// TestAlbumsBrowsesReleaseGroupsForAnArtist exercises the
// release-group?artist={mbid} browse documented in
// docs/research/metadata.md §2.3.
func TestAlbumsBrowsesReleaseGroupsForAnArtist(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/browse_releasegroups_radiohead.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/release-group/", r.URL.Path)
		require.Equal(t, "a74b1b7f-71a5-4011-9441-d0b5e4122711", r.URL.Query().Get("artist"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	albums, err := c.Albums(context.Background(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")

	require.NoError(t, err)
	require.Len(t, albums, 1)
	require.Equal(t, "Kid A", albums[0].Title)
	require.Equal(t, "Album", albums[0].PrimaryType)
	require.True(t, albums[0].ReleaseDate.Equal(time.Date(2000, 10, 2, 0, 0, 0, 0, time.UTC)))
	require.EqualValues(t, 900, albums[0].Ratings["mb"].ValueCentis, "MusicBrainz 4.5/5 normalized to a /10 scale, ×100")
}

// TestAlbumLooksUpASingleReleaseGroup exercises the
// release-group/{mbid}?inc=... lookup documented in
// docs/research/metadata.md §2.3.
func TestAlbumLooksUpASingleReleaseGroup(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/releasegroup_kid_a.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/release-group/0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	album, err := c.Album(context.Background(), "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6")

	require.NoError(t, err)
	require.Equal(t, "Kid A", album.Title)
	require.Equal(t, "Album", album.PrimaryType)
	require.Equal(t, "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6", album.IDs[metadata.KeyMBReleaseGroup])
}

func TestArtistRejectsMalformedResponseBodies(t *testing.T) {
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
			c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
			require.NoError(t, err)

			var a *metadata.Artist
			require.NotPanics(t, func() {
				a, err = c.Artist(context.Background(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")
			})

			require.Nil(t, a)
			require.Error(t, err)
			require.ErrorIs(t, err, metadata.ErrDecode)
		})
	}
}
