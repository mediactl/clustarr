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

package metadata_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// countingAlbumProvider is an ArtistProvider that answers one release
// group and counts how often it is asked, standing for MusicBrainz, whose
// one request a second every Album lookup used to queue on.
type countingAlbumProvider struct {
	stubArtistOnlyProvider
	calls *atomic.Int32
}

func (p countingAlbumProvider) Album(ctx context.Context, id string) (*pkgmetadata.Album, error) {
	p.calls.Add(1)
	return p.stubArtistOnlyProvider.Album(ctx, id)
}

// borderWars is a release group with one release, and on it the track a
// file of the owner's music library is (talos, 2026-11-15).
func borderWars() *pkgmetadata.Album {
	const rg = "22f0c3da-122b-475b-94f8-4123a0a9c4fa"
	return &pkgmetadata.Album{
		IDs:   pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: rg},
		Title: "Border Wars",
		Releases: []pkgmetadata.AlbumRelease{{
			IDs:   pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRelease: "7e867e68-9598-466b-8148-6e05422407f7"},
			Title: "Border Wars", Status: "Official", TrackCount: 1,
			Media: []pkgmetadata.Medium{{Position: 1, Format: "Digital Media", TrackCount: 1, Tracks: []pkgmetadata.Track{{
				IDs:   pkgmetadata.ExternalIDs{pkgmetadata.KeyMBRecording: "a22ed7aa-0765-4855-958e-bd84b493813e"},
				Title: "Thank You", Position: 2, MediumNumber: 1, ArtistCredit: "Berner & The Jacka feat. Liqz & Messy Marv",
			}}}},
		}},
	}
}

// albumHarness is one Album, the metadata worker that refreshes it and the
// lookup RPC an Album's reconcile asks for its track listing, sharing one
// cache as the gateway's do.
type albumHarness struct {
	recordHarness
	handler *metadata.Handler
	calls   *atomic.Int32
	rg      string
}

func newAlbumHarness(t *testing.T, cache pkgmetadata.Cache) albumHarness {
	t.Helper()
	doc := borderWars()
	alb := &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "border-wars", Namespace: "nv", UID: "border-wars"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "berner", ReleaseGroupID: doc.IDs[pkgmetadata.KeyMBReleaseGroup]},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(alb).WithStatusSubresource(alb).Build()
	h := newRecordHarness(t)
	calls := new(atomic.Int32)
	reg := &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{
		countingAlbumProvider{stubArtistOnlyProvider: stubArtistOnlyProvider{album: doc}, calls: calls},
	}}
	stop, err := metadata.ServeRPC(h.bus, reg, cache)
	require.NoError(t, err)
	t.Cleanup(stop)
	return albumHarness{
		recordHarness: h,
		handler: &metadata.Handler{
			Client: c, Reader: c, Cache: cache, Records: h.records,
			Extended: h.bus.KV(events.BucketMetadataExtended), Registry: reg,
		},
		calls: calls,
		rg:    alb.Spec.ReleaseGroupID,
	}
}

// lookup is the Album reconciler's track-listing request.
func (h albumHarness) lookup(t *testing.T) *pkgmetadata.Album {
	t.Helper()
	var resp schema.MetadataResponse
	req := schema.MetadataRequest{Kind: commonv1.MediaKindAlbum, IDs: map[string]string{pkgmetadata.KeyMBReleaseGroup: h.rg}}
	require.NoError(t, h.bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
	require.Empty(t, resp.Error)
	var got pkgmetadata.Album
	require.NoError(t, json.Unmarshal(resp.Result, &got))
	return &got
}

// An Album's track listing is the document the metadata worker cached when
// it refreshed the Album, recordings and all, so the lookup costs no second
// provider call (talos, 2026-11-15: 1,082 of 1,092 albums asked MusicBrainz
// for it every 30 s, queued on its one request a second behind a 10 s
// deadline, and only 10 got an answer in four and a half days, while the
// gateway's cache held every one of them).
func TestAnAlbumLookupIsAnsweredFromTheDocumentTheWorkerCached(t *testing.T) {
	cache, err := pkgmetadata.NewLRUCache(16, clockwork.NewRealClock())
	require.NoError(t, err)
	h := newAlbumHarness(t, cache)

	h.handle(t, h.handler, commonv1.MediaKindAlbum, "nv", "border-wars")
	require.EqualValues(t, 1, h.calls.Load(), "the worker's refresh asks the provider")

	got := h.lookup(t)

	require.Equal(t, borderWars().Releases, got.Releases, "the cached document's releases, tracks included")
	require.EqualValues(t, 1, h.calls.Load(), "the lookup is answered from the cache, the provider is not asked again")
}

// A lookup the cache cannot answer asks the provider, and caches nothing:
// the worker caches a document only after enriching it (crosswalk ids,
// artwork), and a refresh that took the lookup's bare document from the
// cache would serve the Album without them.
func TestAnAlbumLookupTheCacheLacksAsksTheProviderAndLeavesTheCacheToTheWorker(t *testing.T) {
	cache, err := pkgmetadata.NewLRUCache(16, clockwork.NewRealClock())
	require.NoError(t, err)
	h := newAlbumHarness(t, cache)

	got := h.lookup(t)
	require.Equal(t, borderWars().Releases, got.Releases)
	require.EqualValues(t, 1, h.calls.Load(), "a cache miss asks the provider")

	h.handle(t, h.handler, commonv1.MediaKindAlbum, "nv", "border-wars")
	require.EqualValues(t, 2, h.calls.Load(), "the worker's refresh found no document the lookup had cached")

	h.lookup(t)
	require.EqualValues(t, 2, h.calls.Load(), "the worker's document answers the next lookup")
}

// countingListProvider is an ArtistProvider whose artist album list counts
// its calls and can fail.
type countingListProvider struct {
	stubArtistOnlyProvider
	lists  *atomic.Int32
	albums []pkgmetadata.Album
	err    error
}

func (p *countingListProvider) Albums(context.Context, string) ([]pkgmetadata.Album, error) {
	p.lists.Add(1)
	return p.albums, p.err
}

// An Artist's album list is answered from the gateway's cache for a day
// after a fetch, so the listing every Artist's fan-out asks for after a
// manager restart, or a spec edit, costs MusicBrainz nothing; a failed
// fetch is not cached (talos, 2026-11-16: 28 of 145 Artists' listings
// failed for days on MusicBrainz's one request a second, a popular
// artist's list being several pages).
func TestAnArtistsAlbumListIsAnsweredFromTheCacheForADay(t *testing.T) {
	clock := clockwork.NewFakeClock()
	cache, err := pkgmetadata.NewLRUCache(16, clock)
	require.NoError(t, err)
	h := newRecordHarness(t)
	prov := &countingListProvider{
		lists:  new(atomic.Int32),
		albums: []pkgmetadata.Album{{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyMBReleaseGroup: "believe-group"}, Title: "Believe"}},
	}
	stop, err := metadata.ServeRPC(h.bus, &pkgmetadata.Registry{Artists: []pkgmetadata.ArtistProvider{prov}}, cache)
	require.NoError(t, err)
	t.Cleanup(stop)
	ask := func() schema.MetadataResponse {
		t.Helper()
		var resp schema.MetadataResponse
		req := schema.MetadataRequest{Kind: commonv1.MediaKindAlbum, IDs: map[string]string{pkgmetadata.KeyMBArtist: "cher-mbid"}}
		require.NoError(t, h.bus.Request(context.Background(), events.RPCMetadataLookup, req, &resp))
		return resp
	}
	titles := func(resp schema.MetadataResponse) []string {
		t.Helper()
		require.Empty(t, resp.Error)
		var out []string
		for _, raw := range resp.Results {
			var a pkgmetadata.Album
			require.NoError(t, json.Unmarshal(raw, &a))
			out = append(out, a.Title)
		}
		return out
	}

	assert.Equal(t, []string{"Believe"}, titles(ask()))
	assert.Equal(t, []string{"Believe"}, titles(ask()))
	require.EqualValues(t, 1, prov.lists.Load(), "the second listing is the cache's")

	clock.Advance(25 * time.Hour)
	assert.Equal(t, []string{"Believe"}, titles(ask()))
	require.EqualValues(t, 2, prov.lists.Load(), "a day on, the list is fetched again")

	clock.Advance(25 * time.Hour)
	prov.err = errors.New("rate: Wait(n=1) would exceed context deadline")
	assert.NotEmpty(t, ask().Error)
	prov.err = nil
	assert.Equal(t, []string{"Believe"}, titles(ask()))
	assert.EqualValues(t, 4, prov.lists.Load(), "a failed fetch caches nothing")
}
