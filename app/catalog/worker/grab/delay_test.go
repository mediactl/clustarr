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

package grab

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

func TestMediaKeyDelegatesToEvents(t *testing.T) {
	ref := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"}
	assert.Equal(t, events.MediaKey("movie", "media", "the-thing-1982"), MediaKey("media", ref))
}

// TestMediaKeyIsKindScoped is the collision guard events.MediaKey exists for,
// asserted from this package's own vocabulary: a Movie and a Series of the
// same name must not share one grab lease.
func TestMediaKeyIsKindScoped(t *testing.T) {
	movie := MediaKey("media", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "thing"})
	series := MediaKey("media", commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "thing"})
	assert.NotEqual(t, movie, series)
	assert.NotEqual(t, events.LeaseKey(movie), events.LeaseKey(series))
}

func TestStatusTargets(t *testing.T) {
	cases := []struct {
		name    string
		target  commonv1.MediaRef
		keys    []string
		want    []commonv1.MediaRef
		wantErr error
	}{
		{
			name:   "movie singleton",
			target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"},
			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"}},
		},
		{
			name:   "episode singleton",
			target: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"},
			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"}},
		},
		{
			name:   "season pack expands to one MediaRef per key",
			target: commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"},
			keys:   []string{"the-wire-s01e01", "the-wire-s01e02"},
			want: []commonv1.MediaRef{
				{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"},
				{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e02"},
			},
		},
		{
			name:    "series with no keys takes no leases and is refused",
			target:  commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"},
			wantErr: ErrUnsupportedKind,
		},
		{
			name:    "movie with keys is not a pack shape",
			target:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"},
			keys:    []string{"x"},
			wantErr: ErrUnsupportedKind,
		},
		{
			name:   "album singleton",
			target: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "radiohead-kid-a"},
			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindAlbum, Name: "radiohead-kid-a"}},
		},
		{
			name:   "book singleton",
			target: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "dune"},
			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindBook, Name: "dune"}},
		},
		{
			name:   "audiobook singleton",
			target: commonv1.MediaRef{Kind: commonv1.MediaKindAudiobook, Name: "guards-guards"},
			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindAudiobook, Name: "guards-guards"}},
		},
		{
			name:   "issue singleton",
			target: commonv1.MediaRef{Kind: commonv1.MediaKindIssue, Name: "saga-00001.0"},
			want:   []commonv1.MediaRef{{Kind: commonv1.MediaKindIssue, Name: "saga-00001.0"}},
		},
		{
			name:    "an album with keys is not a pack shape",
			target:  commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: "radiohead-kid-a"},
			keys:    []string{"x"},
			wantErr: ErrUnsupportedKind,
		},
		{
			name:    "an artist is a container, never grabbed",
			target:  commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "radiohead"},
			wantErr: ErrUnsupportedKind,
		},
		{
			name:    "a comic is a container, never grabbed",
			target:  commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: "saga"},
			keys:    []string{"saga-00001.0"},
			wantErr: ErrUnsupportedKind,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := StatusTargets(c.target, c.keys)
			if c.wantErr != nil {
				require.ErrorIs(t, err, c.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

func TestBypasses(t *testing.T) {
	cases := []struct {
		name        string
		spec        catalogv1alpha1.DelayProfileSpec
		atTopTier   bool
		formatScore int32
		want        bool
	}{
		{"defaulted profile, top tier bypasses", catalogv1alpha1.DelayProfileSpec{}, true, 0, true},
		{"defaulted profile, not top tier does not bypass", catalogv1alpha1.DelayProfileSpec{}, false, 999, false},
		{"BypassIfHighestQuality explicitly off", catalogv1alpha1.DelayProfileSpec{BypassIfHighestQuality: ptr.To(false)}, true, 0, false},
		{
			"BypassIfAboveFormatScore at the minimum bypasses",
			catalogv1alpha1.DelayProfileSpec{
				BypassIfHighestQuality: ptr.To(false), BypassIfAboveFormatScore: ptr.To(true), MinimumFormatScore: 100,
			},
			false, 100, true,
		},
		{
			"BypassIfAboveFormatScore below the minimum does not bypass",
			catalogv1alpha1.DelayProfileSpec{
				BypassIfHighestQuality: ptr.To(false), BypassIfAboveFormatScore: ptr.To(true), MinimumFormatScore: 100,
			},
			false, 99, false,
		},
		{
			"an explicitly-set profile from the apiserver never sees a nil default",
			catalogv1alpha1.DelayProfileSpec{
				BypassIfHighestQuality: ptr.To(true), BypassIfAboveFormatScore: ptr.To(false),
			},
			false, 10_000, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, Bypasses(c.spec, c.atTopTier, c.formatScore))
		})
	}
}

func TestDelayFor(t *testing.T) {
	spec := catalogv1alpha1.DelayProfileSpec{UsenetDelayMinutes: 15, TorrentDelayMinutes: 45}
	assert.Equal(t, 15*time.Minute, DelayFor(spec, commonv1.ProtocolUsenet))
	assert.Equal(t, 45*time.Minute, DelayFor(spec, commonv1.ProtocolTorrent))
	assert.Zero(t, DelayFor(spec, commonv1.Protocol("carrier-pigeon")))
	assert.Zero(t, DelayFor(catalogv1alpha1.DelayProfileSpec{}, commonv1.ProtocolTorrent))
}

func TestProtocolEnabled(t *testing.T) {
	assert.True(t, ProtocolEnabled(catalogv1alpha1.DelayProfileSpec{}, commonv1.ProtocolUsenet))
	assert.True(t, ProtocolEnabled(catalogv1alpha1.DelayProfileSpec{}, commonv1.ProtocolTorrent))
	assert.False(t, ProtocolEnabled(catalogv1alpha1.DelayProfileSpec{EnableTorrent: ptr.To(false)}, commonv1.ProtocolTorrent))
	assert.False(t, ProtocolEnabled(catalogv1alpha1.DelayProfileSpec{}, commonv1.Protocol("")))
}

// TestMediaKeyFor_PacksOfOneSeriesDoNotCollide is the pack-key regression:
// events.MediaKey takes only kind/namespace/name, so without the episode
// digest an S01 pack and an S02 pack of one series shared one
// clustarr-pending entry and one Msg-Id -- the loser was evicted on quality
// alone and its scheduled delivery deduplicated away.
func TestMediaKeyFor_PacksOfOneSeriesDoNotCollide(t *testing.T) {
	series := commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"}
	s1 := MediaKeyFor("media", series, []string{"the-wire-s01e01", "the-wire-s01e02"})
	s2 := MediaKeyFor("media", series, []string{"the-wire-s02e01", "the-wire-s02e02"})
	assert.NotEqual(t, s1, s2, "two seasons of one series must not share a pending entry")
	assert.NotEqual(t, events.PendingKey(s1), events.PendingKey(s2))
	assert.NotEqual(t, events.WorkGrabSubject(s1), events.WorkGrabSubject(s2))
}

func TestMediaKeyFor_IsOrderInsensitiveAndStable(t *testing.T) {
	series := commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"}
	forward := MediaKeyFor("media", series, []string{"the-wire-s01e01", "the-wire-s01e02"})
	reversed := MediaKeyFor("media", series, []string{"the-wire-s01e02", "the-wire-s01e01"})
	assert.Equal(t, forward, reversed, "the same pack described in a different order is one grab")

	// A partial pack is a different grab from the full one.
	partial := MediaKeyFor("media", series, []string{"the-wire-s01e01"})
	assert.NotEqual(t, forward, partial)
}

// TestMediaKeyFor_SingletonIsUnchanged: a movie or a single episode has no
// keys, so its token stays exactly events.MediaKey's -- the search worker and
// anything else deriving it without keys must still agree.
func TestMediaKeyFor_SingletonIsUnchanged(t *testing.T) {
	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-thing-1982"}
	assert.Equal(t, MediaKey("media", movie), MediaKeyFor("media", movie, nil))
	assert.Equal(t, MediaKey("media", movie), MediaKeyFor("media", movie, []string{}))
}

// TestMediaKeyFor_DoesNotCollideWithASingleEpisodesOwnKey guards the one
// pathological case the digest exists for: a pack whose synthetic name could
// otherwise be mistaken for a real object's.
func TestMediaKeyFor_DoesNotCollideWithASingleEpisodesOwnKey(t *testing.T) {
	series := commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-wire"}
	pack := MediaKeyFor("media", series, []string{"the-wire-s01e01"})
	single := MediaKey("media", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01"})
	assert.NotEqual(t, pack, single)
	assert.NotEqual(t, events.LeaseKey(pack), events.LeaseKey(single),
		"a pack's own token must never be mistaken for one of its episodes' lease keys")
}
