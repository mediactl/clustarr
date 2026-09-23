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

package downloads

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

const hexHash = "0123456789abcdef0123456789abcdef01234567"

func TestResolveSource(t *testing.T) {
	tests := []struct {
		name string
		rel  commonv1.ReleaseInfo
		want downloadv1alpha1.DownloadSource
	}{
		{
			name: "a magnet wins and carries the info hash",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolTorrent, GUID: "g", IndexerRef: "idx",
				MagnetURL: "magnet:?xt=urn:btih:" + hexHash, DownloadURL: "https://idx/dl?apikey=k",
				InfoHash: hexHash,
			},
			want: downloadv1alpha1.DownloadSource{
				MagnetURL:        ptr.To("magnet:?xt=urn:btih:" + hexHash),
				ExpectedInfoHash: ptr.To(hexHash),
			},
		},
		{
			name: "an indexer release without a magnet goes through indexarr, whatever its auth",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolTorrent, GUID: "g", IndexerRef: "idx",
				DownloadURL: "https://idx/dl?apikey=k", InfoHash: hexHash,
			},
			want: downloadv1alpha1.DownloadSource{
				IndexerDownload:  &downloadv1alpha1.IndexerDownload{IndexerRef: "idx", GUID: "g", URL: "https://idx/dl?apikey=k"},
				ExpectedInfoHash: ptr.To(hexHash),
			},
		},
		{
			name: "a usenet indexer release goes through indexarr and never carries a hash",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolUsenet, GUID: "g", IndexerRef: "idx",
				DownloadURL: "https://idx/nzb", InfoHash: hexHash,
			},
			want: downloadv1alpha1.DownloadSource{
				IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "idx", GUID: "g", URL: "https://idx/nzb"},
			},
		},
		{
			name: "a torrent no indexer stands behind is fetched directly",
			rel:  commonv1.ReleaseInfo{Protocol: commonv1.ProtocolTorrent, GUID: "g", DownloadURL: "https://x/t.torrent"},
			want: downloadv1alpha1.DownloadSource{TorrentURL: ptr.To("https://x/t.torrent")},
		},
		{
			name: "an nzb no indexer stands behind is fetched directly",
			rel:  commonv1.ReleaseInfo{Protocol: commonv1.ProtocolUsenet, IndexerRef: "idx", DownloadURL: "https://x/a.nzb"},
			want: downloadv1alpha1.DownloadSource{NZBURL: ptr.To("https://x/a.nzb")},
		},
		{
			name: "a magnet field that is not a magnet falls through to indexarr",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolTorrent, GUID: "g", IndexerRef: "idx",
				MagnetURL: "https://aggregator/redirect/123",
			},
			want: downloadv1alpha1.DownloadSource{
				IndexerDownload: &downloadv1alpha1.IndexerDownload{IndexerRef: "idx", GUID: "g"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveSource(tc.rel)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, 1, members(got), "DownloadSource's CEL rule requires exactly one member")
		})
	}
}

func members(s downloadv1alpha1.DownloadSource) int {
	n := 0
	for _, set := range []bool{s.MagnetURL != nil, s.TorrentURL != nil, s.NZBURL != nil, s.IndexerDownload != nil} {
		if set {
			n++
		}
	}
	return n
}

func TestResolveSource_NothingToFetchIsErrNoSource(t *testing.T) {
	_, err := ResolveSource(commonv1.ReleaseInfo{Protocol: commonv1.ProtocolTorrent, GUID: "g"})
	require.ErrorIs(t, err, ErrNoSource)
}

// TestExpectedInfoHash pins the forms DownloadSource.ExpectedInfoHash's CRD
// pattern (^[0-9a-f]{40}([0-9a-f]{24})?$) accepts. An upper-case v1 hash
// passed through verbatim got the whole Download rejected by the apiserver.
func TestExpectedInfoHash(t *testing.T) {
	v2 := hexHash + "0123456789abcdef01234567"
	tests := []struct {
		in, want string
		ok       bool
	}{
		{in: hexHash, want: hexHash, ok: true},
		{in: "0123456789ABCDEF0123456789ABCDEF01234567", want: hexHash, ok: true},
		{in: v2, want: v2, ok: true},
		// base32 of the 20 bytes hexHash encodes.
		{in: "AERUKZ4JVPG66AJDIVTYTK6N54ASGRLH", want: hexHash, ok: true},
		{in: "aeruKZ4JVPG66AJDIVTYTK6N54ASGRLH", want: hexHash, ok: true},
		{in: "", ok: false},
		{in: "not-a-hash", ok: false},
		{in: "zz23456789abcdef0123456789abcdef01234567", ok: false},
	}
	for _, tc := range tests {
		got, ok := expectedInfoHash(tc.in)
		assert.Equalf(t, tc.ok, ok, "expectedInfoHash(%q)", tc.in)
		assert.Equalf(t, tc.want, got, "expectedInfoHash(%q)", tc.in)
	}
}

func TestSourceApplyConfigurationRoundTripsEveryMember(t *testing.T) {
	src := downloadv1alpha1.DownloadSource{
		IndexerDownload:  &downloadv1alpha1.IndexerDownload{IndexerRef: "idx", GUID: "g", URL: "u"},
		ExpectedInfoHash: ptr.To(hexHash),
	}
	ac := SourceApplyConfiguration(src)
	require.NotNil(t, ac.IndexerDownload)
	assert.Equal(t, "idx", *ac.IndexerDownload.IndexerRef)
	assert.Equal(t, "g", *ac.IndexerDownload.GUID)
	assert.Equal(t, "u", *ac.IndexerDownload.URL)
	assert.Equal(t, hexHash, *ac.ExpectedInfoHash)
	assert.Nil(t, ac.MagnetURL)

	direct := SourceApplyConfiguration(downloadv1alpha1.DownloadSource{TorrentURL: ptr.To("t"), NZBURL: ptr.To("n"), MagnetURL: ptr.To("m")})
	assert.Equal(t, "t", *direct.TorrentURL)
	assert.Equal(t, "n", *direct.NZBURL)
	assert.Equal(t, "m", *direct.MagnetURL)
}

func TestIsTerminal(t *testing.T) {
	terminal := map[downloadv1alpha1.DownloadPhase]bool{
		downloadv1alpha1.DownloadPhaseImported:    true,
		downloadv1alpha1.DownloadPhaseFailed:      true,
		downloadv1alpha1.DownloadPhaseBlocklisted: true,
		downloadv1alpha1.DownloadPhaseRemoving:    true,
	}
	for _, p := range []downloadv1alpha1.DownloadPhase{
		"",
		downloadv1alpha1.DownloadPhasePending, downloadv1alpha1.DownloadPhaseAssigned,
		downloadv1alpha1.DownloadPhaseQueued, downloadv1alpha1.DownloadPhaseDownloading,
		downloadv1alpha1.DownloadPhasePaused, downloadv1alpha1.DownloadPhaseCompleted,
		downloadv1alpha1.DownloadPhaseSeeding, downloadv1alpha1.DownloadPhaseImported,
		downloadv1alpha1.DownloadPhaseFailed, downloadv1alpha1.DownloadPhaseBlocklisted,
		downloadv1alpha1.DownloadPhaseRemoving,
	} {
		assert.Equalf(t, terminal[p], IsTerminal(p), "phase %q", p)
	}

	deleting := &downloadv1alpha1.Download{ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: ptr.To(metav1.Now())}}
	assert.False(t, IsActive(deleting), "a Download being deleted no longer occupies its item")
	assert.True(t, IsActive(&downloadv1alpha1.Download{}), "a Download grabarr has not admitted yet does")
}

func TestCovers(t *testing.T) {
	movie := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{UID: "movie-uid"}}},
		Spec:       downloadv1alpha1.DownloadSpec{Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "alien"}},
	}
	pack := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{{UID: "series-uid"}}},
		Spec: downloadv1alpha1.DownloadSpec{Target: commonv1.MediaRef{
			Kind: commonv1.MediaKindSeries, Name: "the-wire", Keys: []string{"the-wire-s01e01", "the-wire-s01e02"},
		}},
	}
	orphan := &downloadv1alpha1.Download{
		Spec: downloadv1alpha1.DownloadSpec{Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "alien"}},
	}

	assert.True(t, Covers(movie, commonv1.MediaKindMovie, "alien", "movie-uid"))
	assert.True(t, Covers(movie, commonv1.MediaKindMovie, "renamed", "movie-uid"), "the ownerReference alone is enough")
	assert.True(t, Covers(orphan, commonv1.MediaKindMovie, "alien", "movie-uid"),
		"a Download the Search controller made while the item was missing has no owner but still targets it")
	assert.False(t, Covers(movie, commonv1.MediaKindMovie, "aliens", "other-uid"))
	assert.False(t, Covers(movie, commonv1.MediaKindEpisode, "alien", ""), "kind is part of the identity")

	assert.True(t, Covers(pack, commonv1.MediaKindEpisode, "the-wire-s01e02", "episode-uid"),
		"an episode inside a pack is covered although the Series owns the Download")
	assert.False(t, Covers(pack, commonv1.MediaKindEpisode, "the-wire-s01e03", "episode-uid"))
	assert.True(t, Covers(pack, commonv1.MediaKindSeries, "the-wire", ""))
}
