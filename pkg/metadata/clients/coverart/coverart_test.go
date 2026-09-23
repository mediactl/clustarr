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

package coverart_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/coverart"
)

const fixtures = "../../../../testdata/metadata/coverart/"

// serve answers path with the named fixture and 404s everything else,
// recording every path it was asked for.
func serve(t *testing.T, routes map[string]string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		name, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile(fixtures + name)
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestArtworkMapsTheFrontCoverAndTheDiscAndNothingElse(t *testing.T) {
	const rel = "76df3287-6cda-33eb-8e9a-044b5e15ffdd" // Portishead, "Dummy": six images
	srv, _ := serve(t, map[string]string{"/release/" + rel: "release_" + rel + ".json"})
	c := coverart.New(coverart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})

	imgs, err := c.Artwork(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBRelease: rel})

	require.NoError(t, err)
	// Six images: Front(front=true), Front+Booklet(front=false), Booklet,
	// Booklet, Back, Medium. Only the primary front and the medium map.
	require.Equal(t, []metadata.Image{
		{Type: metadata.ImageTypePoster, URL: "https://coverartarchive.org/release/" + rel + "/829521842.jpg"},
		{Type: metadata.ImageTypeDisc, URL: "https://coverartarchive.org/release/" + rel + "/5769316809.jpg"},
	}, imgs)
}

func TestArtworkPrefersTheReleaseGroup(t *testing.T) {
	const rg = "1b022e01-4da6-387b-8658-8678046e4cef"
	srv, seen := serve(t, map[string]string{"/release-group/" + rg: "release-group_" + rg + ".json"})
	c := coverart.New(coverart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})

	imgs, err := c.Artwork(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{
		metadata.KeyMBReleaseGroup: rg,
		metadata.KeyMBRelease:      "76df3287-6cda-33eb-8e9a-044b5e15ffdd",
	})

	require.NoError(t, err)
	require.Equal(t, []string{"/release-group/" + rg}, *seen)
	require.Len(t, imgs, 1)
	require.Equal(t, metadata.ImageTypePoster, imgs[0].Type)
}

func TestArtworkMapsA404ToErrNotFound(t *testing.T) {
	srv, _ := serve(t, nil)
	c := coverart.New(coverart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})

	_, err := c.Artwork(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: "00000000-0000-0000-0000-000000000000"})

	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestArtworkRefusesWhatItCannotServeWithoutARequest(t *testing.T) {
	srv, seen := serve(t, nil)
	c := coverart.New(coverart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
	ctx := context.Background()

	_, err := c.Artwork(ctx, commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "603"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	require.NotErrorIs(t, err, metadata.ErrNotFound, "a kind the CAA cannot hold is not a missing record")

	_, err = c.Artwork(ctx, commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBArtist: "a74b1b7f-71a5-4011-9441-d0b5e4122711"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)

	_, err = c.Artwork(ctx, commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: "../../admin"})
	require.ErrorIs(t, err, coverart.ErrInvalidMBID)

	require.Empty(t, *seen)
}

func TestPingTreatsA404AsReachable(t *testing.T) {
	srv, _ := serve(t, nil)
	c := coverart.New(coverart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})
	require.NoError(t, c.Ping(context.Background()))

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	require.Error(t, coverart.New(coverart.Config{HTTPClient: down.Client(), BaseURL: down.URL}).Ping(context.Background()))
}

func TestArtworkLeavesOutAnImageTheCAAHasNotApproved(t *testing.T) {
	const rg = "1b022e01-4da6-387b-8658-8678046e4cef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"images":[
		 {"approved":false,"front":true,"back":false,"types":["Front"],"image":"http://coverartarchive.org/release/x/1.jpg"},
		 {"approved":true,"front":false,"back":false,"types":["Medium"],"image":"http://coverartarchive.org/release/x/2.jpg"}
		],"release":"https://musicbrainz.org/release/x"}`))
	}))
	defer srv.Close()
	c := coverart.New(coverart.Config{HTTPClient: srv.Client(), BaseURL: srv.URL})

	imgs, err := c.Artwork(context.Background(), commonv1.MediaKindAlbum, metadata.ExternalIDs{metadata.KeyMBReleaseGroup: rg})

	require.NoError(t, err)
	require.Equal(t, []metadata.Image{{Type: metadata.ImageTypeDisc, URL: "https://coverartarchive.org/release/x/2.jpg"}}, imgs)
}
