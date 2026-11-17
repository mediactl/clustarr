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

package metadata

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/trailers"
)

// fakeTrailers is a trailer provider answering what the test sets.
type fakeTrailers struct {
	videos []trailers.Video
	err    error
	asked  []pkgmetadata.ExternalIDs
}

func (f *fakeTrailers) Trailers(_ context.Context, _ commonv1.MediaKind, ids pkgmetadata.ExternalIDs) ([]trailers.Video, error) {
	f.asked = append(f.asked, ids)
	return f.videos, f.err
}

func askTrailers(t *testing.T, providers []pkgmetadata.TrailerProvider, req schema.TrailersRequest) schema.TrailersResponse {
	t.Helper()
	bus := newTestBus(t)
	stop, err := ServeTrailers(bus, &pkgmetadata.Registry{Trailers: providers})
	require.NoError(t, err)
	t.Cleanup(stop)
	var resp schema.TrailersResponse
	require.NoError(t, bus.Request(context.Background(), events.RPCMetadataTrailers, req, &resp))
	return resp
}

// The gateway answers every provider's videos in the providers' order,
// each asked with the item's ids.
func TestTrailersAreEveryProvidersVideos(t *testing.T) {
	tmdb := &fakeTrailers{videos: []trailers.Video{{Site: "YouTube", Key: "tmdb0001", Provider: "tmdb"}}}
	tvdb := &fakeTrailers{videos: []trailers.Video{{Site: "YouTube", Key: "tvdb0001", Provider: "tvdb"}}}
	ids := map[string]string{"tmdb": "76331", "tvdb": "338186"}
	resp := askTrailers(t, []pkgmetadata.TrailerProvider{tmdb, tvdb}, schema.TrailersRequest{Kind: commonv1.MediaKindSeries, IDs: ids})
	require.Empty(t, resp.Error)
	require.Len(t, resp.Videos, 2)
	assert.Equal(t, "tmdb0001", resp.Videos[0].Key)
	assert.Equal(t, "tvdb0001", resp.Videos[1].Key)
	assert.Equal(t, []pkgmetadata.ExternalIDs{ids}, tmdb.asked)
}

// "None" is answered only when it is known: a provider that could not be
// asked is an error unless another named a video, and an item no provider
// names a video for is an empty list.
func TestTrailersAreNoneOnlyWhenEveryProviderAnswered(t *testing.T) {
	down := &fakeTrailers{err: errors.New("tmdb: 503")}
	empty := &fakeTrailers{}
	has := &fakeTrailers{videos: []trailers.Video{{Site: "YouTube", Key: "tvdb0001"}}}
	req := schema.TrailersRequest{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"tmdb": "1"}}

	resp := askTrailers(t, []pkgmetadata.TrailerProvider{down, empty}, req)
	assert.Contains(t, resp.Error, "tmdb: 503")
	assert.Empty(t, resp.Videos)

	resp = askTrailers(t, []pkgmetadata.TrailerProvider{down, has}, req)
	assert.Empty(t, resp.Error)
	assert.Len(t, resp.Videos, 1)

	resp = askTrailers(t, []pkgmetadata.TrailerProvider{empty}, req)
	assert.Empty(t, resp.Error)
	assert.NotNil(t, resp.Videos)
	assert.Empty(t, resp.Videos)

	resp = askTrailers(t, nil, req)
	assert.NotEmpty(t, resp.Error, "no provider is not none")
	resp = askTrailers(t, []pkgmetadata.TrailerProvider{has}, schema.TrailersRequest{Kind: commonv1.MediaKindArtist})
	assert.NotEmpty(t, resp.Error)
}
