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
	"bytes"
	"context"
	"fmt"
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

func TestSearchArtistsSendsAPlainTextDismaxQueryAndMapsHits(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/search_artist_radiohead.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/artist/", r.URL.Path)
		require.Equal(t, "AC/DC && radiohead", r.URL.Query().Get("query"), "the query is sent verbatim; dismax is what neutralises Lucene syntax")
		require.Equal(t, "true", r.URL.Query().Get("dismax"))
		require.Equal(t, "25", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	hits, err := c.SearchArtists(context.Background(), "AC/DC && radiohead")

	require.NoError(t, err)
	require.Equal(t, []metadata.SearchHit{
		{IDs: metadata.ExternalIDs{metadata.KeyMBArtist: "a74b1b7f-71a5-4011-9441-d0b5e4122711"}, Title: "Radiohead", Year: 1991},
		{IDs: metadata.ExternalIDs{metadata.KeyMBArtist: "c74f4726-2671-4011-81b6-f70da905c05a"}, Title: "On a Friday", Year: 1985},
	}, hits)
}

func TestSearchArtistsMapsProviderErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	}))
	defer srv.Close()
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.SearchArtists(context.Background(), "radiohead")
	require.ErrorIs(t, err, metadata.ErrNotFound)
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
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/release-group/0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6":
			_, _ = w.Write(body)
		case "/release/":
			_, _ = w.Write([]byte(`{"release-count":0,"release-offset":0,"releases":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	album, err := c.Album(context.Background(), "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6")

	require.NoError(t, err)
	require.Equal(t, "Kid A", album.Title)
	require.Equal(t, "Album", album.PrimaryType)
	require.Equal(t, "0b56cf2b-8e64-39e0-b6d5-9a89e46be9f6", album.IDs[metadata.KeyMBReleaseGroup])
	require.Empty(t, album.Releases)
}

const theBendsMBID = "b8048f24-c026-3398-b23a-b5e50716cbc7"

// theBendsServer serves The Bends' release group and its releases, recorded
// from musicbrainz.org (trimmed to two tracks per medium) and split across
// two browse pages by offset, the way MusicBrainz pages a release browse.
// It records every browse offset it was asked for.
func theBendsServer(t *testing.T, offsets *[]string) *httptest.Server {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/" + name)
		require.NoError(t, err)
		return b
	}
	rg, p1, p2 := read("releasegroup_the_bends.json"), read("browse_releases_the_bends_p1.json"), read("browse_releases_the_bends_p2.json")
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/release-group/" + theBendsMBID:
			_, _ = w.Write(rg)
		case "/release/":
			q := r.URL.Query() // "+" in inc decodes to a space
			if q.Get("release-group") != theBendsMBID || q.Get("inc") != "artist-credits labels media recordings" || q.Get("limit") != "100" {
				http.Error(w, `{"error":"unexpected browse `+r.URL.RawQuery+`"}`, http.StatusBadRequest)
				return
			}
			*offsets = append(*offsets, q.Get("offset"))
			if q.Get("offset") == "0" {
				_, _ = w.Write(p1)
			} else {
				_, _ = w.Write(p2)
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

// TestAlbumPopulatesReleasesMediaAndTracks is the regression for mapAlbum
// never populating Album.Releases, which left every Album's status.tracks
// empty in production.
func TestAlbumPopulatesReleasesMediaAndTracks(t *testing.T) {
	var offsets []string
	srv := theBendsServer(t, &offsets)
	defer srv.Close()
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	album, err := c.Album(context.Background(), theBendsMBID)
	require.NoError(t, err)

	require.Equal(t, "The Bends", album.Title)
	require.Equal(t, []string{"0", "2"}, offsets, "the second page starts where the first ended")
	require.Len(t, album.Releases, 3)

	promo := album.Releases[0]
	require.Equal(t, "66336036-055a-4b7a-9b4b-ee4ca1b57c92", promo.IDs[metadata.KeyMBRelease])
	require.Equal(t, "The Bends", promo.Title)
	require.Equal(t, "revised no compression", promo.Disambiguation)
	require.Equal(t, "Promotion", promo.Status, "MusicBrainz's status string is passed through unchanged")
	require.True(t, promo.Date.Equal(time.Date(1994, 11, 29, 0, 0, 0, 0, time.UTC)))
	require.Equal(t, []string{"GB"}, promo.Country)
	require.Equal(t, []string{"[no label]"}, promo.Labels)
	require.Empty(t, promo.CatalogNo, `MusicBrainz's "[none]" placeholder is not a catalogue number`)
	require.EqualValues(t, 2, promo.TrackCount)
	require.Len(t, promo.Media, 1)
	require.Equal(t, metadata.Medium{
		Position:   1,
		Format:     "CD-R",
		TrackCount: 2,
		Tracks: []metadata.Track{
			{
				IDs:          metadata.ExternalIDs{metadata.KeyMBRecording: "49bb3701-1b75-4232-8999-ff5318adf676"},
				Title:        "Planet Zerox", // the track title on this release, not the recording's "Planet Telex"
				Position:     1,
				MediumNumber: 1,
				Duration:     258880 * time.Millisecond,
				ArtistCredit: "Radiohead",
			},
			{
				IDs:          metadata.ExternalIDs{metadata.KeyMBRecording: "ad922fb0-f2ac-4d47-803f-a61cfa69117e"},
				Title:        "The Bends",
				Position:     2,
				MediumNumber: 1,
				Duration:     245600 * time.Millisecond,
				ArtistCredit: "Radiohead",
			},
		},
	}, promo.Media[0])

	repress := album.Releases[1]
	require.Equal(t, "746bf2f6-2ba4-3902-be25-84657182bfdf", repress.IDs[metadata.KeyMBRelease])
	require.Equal(t, "Official", repress.Status)
	require.Equal(t, []string{"Parlophone"}, repress.Labels)
	require.Equal(t, "PCS 7372", repress.CatalogNo)
	require.Equal(t, `12" Vinyl`, repress.Media[0].Format)

	japan := album.Releases[2]
	require.Equal(t, "b3b28d75-e474-43b8-a9ec-4c6856fc10b2", japan.IDs[metadata.KeyMBRelease])
	require.Equal(t, []string{"JP"}, japan.Country)
	require.Equal(t, "TOCP-8489", japan.CatalogNo)
}

func TestAlbumFailsWhenTheReleaseBrowseFails(t *testing.T) {
	body, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/releasegroup_the_bends.json")
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/release-group/"+theBendsMBID {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"Not Found"}`))
	}))
	defer srv.Close()
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	album, err := c.Album(context.Background(), theBendsMBID)

	require.Nil(t, album, "an Album with no releases would read downstream as an album that has none")
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestAlbumStopsBrowsingAfterMaxReleasePages(t *testing.T) {
	rg, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/releasegroup_the_bends.json")
	require.NoError(t, err)
	var browses int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/release/" {
			_, _ = w.Write(rg)
			return
		}
		browses++
		offset := r.URL.Query().Get("offset")
		_, _ = w.Write([]byte(`{"release-count":100000,"release-offset":` + offset + `,"releases":[{"id":"00000000-0000-0000-0000-0000000000` + fmt.Sprintf("%02d", browses) + `","title":"Page"}]}`))
	}))
	defer srv.Close()
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	album, err := c.Album(context.Background(), theBendsMBID)

	require.NoError(t, err)
	require.Equal(t, 10, browses, "one refresh must not become an unbounded crawl")
	require.Len(t, album.Releases, 10)
}

// TestAnUnreachableServerIsNotADecodeError is the regression for mapError
// mapping every ClientError{StatusCode: 0} to ErrDecode: musicbrainzws2
// reports a refused connection with that same shape.
func TestAnUnreachableServerIsNotADecodeError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)
	srv.Close()

	_, err = c.Artist(context.Background(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")

	require.Error(t, err)
	require.NotErrorIs(t, err, metadata.ErrDecode, "a refused connection is not a malformed response")
}

func TestACancelledContextSurfacesAsContextCanceled(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = c.Artist(ctx, "a74b1b7f-71a5-4011-9441-d0b5e4122711")

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, metadata.ErrDecode)
}

func TestArtistRejectsAnOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"a74b1b7f-71a5-4011-9441-d0b5e4122711","name":"`))
		_, _ = w.Write(bytes.Repeat([]byte("x"), int(metadata.MaxResponseBytes)))
		_, _ = w.Write([]byte(`"}`))
	}))
	defer srv.Close()
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	_, err = c.Artist(context.Background(), "a74b1b7f-71a5-4011-9441-d0b5e4122711")

	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
	require.NotErrorIs(t, err, metadata.ErrDecode)
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

// TestAlbumStopsAfterASinglePageThatHoldsEveryRelease: release-count equal
// to what one page returned ends the browse. The fixture is also the one a
// stub server can answer any release browse with (the paging pair above
// needs offset routing).
func TestAlbumStopsAfterASinglePageThatHoldsEveryRelease(t *testing.T) {
	rg, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/releasegroup_the_bends.json")
	require.NoError(t, err)
	page, err := os.ReadFile("../../../../testdata/metadata/musicbrainz/browse_releases_the_bends.json")
	require.NoError(t, err)
	var browses int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/release/" {
			browses++
			_, _ = w.Write(page)
			return
		}
		_, _ = w.Write(rg)
	}))
	defer srv.Close()
	c, err := musicbrainz.New("Clustarr/0.1 (https://github.com/mediactl/clustarr)", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))
	require.NoError(t, err)

	album, err := c.Album(context.Background(), theBendsMBID)

	require.NoError(t, err)
	require.Equal(t, 1, browses)
	require.Len(t, album.Releases, 3)
}
