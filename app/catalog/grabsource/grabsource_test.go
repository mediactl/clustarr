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

package grabsource

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

const hexHash = "0123456789abcdef0123456789abcdef01234567"

func TestResolveSource(t *testing.T) {
	tests := []struct {
		name string
		rel  commonv1.ReleaseInfo
		want commonv1.DownloadSource
	}{
		{
			name: "a magnet wins and carries the info hash",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolTorrent, GUID: "g", IndexerRef: "idx",
				MagnetURL: "magnet:?xt=urn:btih:" + hexHash, DownloadURL: "https://idx/dl?apikey=k",
				InfoHash: hexHash,
			},
			want: commonv1.DownloadSource{
				MagnetURL:        new("magnet:?xt=urn:btih:" + hexHash),
				ExpectedInfoHash: new(hexHash),
			},
		},
		{
			name: "an indexer release without a magnet goes through index, whatever its auth",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolTorrent, GUID: "g", IndexerRef: "idx",
				DownloadURL: "https://idx/dl?apikey=k", InfoHash: hexHash,
			},
			want: commonv1.DownloadSource{
				IndexerDownload:  &commonv1.IndexerDownload{IndexerRef: "idx", GUID: "g", URL: "https://idx/dl?apikey=k"},
				ExpectedInfoHash: new(hexHash),
			},
		},
		{
			name: "an upper-case info hash is lower-cased to fit the CRD pattern",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolTorrent, GUID: "g", IndexerRef: "idx",
				InfoHash: "0123456789ABCDEF0123456789ABCDEF01234567",
			},
			want: commonv1.DownloadSource{
				IndexerDownload:  &commonv1.IndexerDownload{IndexerRef: "idx", GUID: "g"},
				ExpectedInfoHash: new(hexHash),
			},
		},
		{
			name: "a usenet indexer release goes through index and never carries a hash",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolUsenet, GUID: "g", IndexerRef: "idx",
				DownloadURL: "https://idx/nzb", InfoHash: hexHash,
			},
			want: commonv1.DownloadSource{
				IndexerDownload: &commonv1.IndexerDownload{IndexerRef: "idx", GUID: "g", URL: "https://idx/nzb"},
			},
		},
		{
			name: "a torrent no indexer stands behind is fetched directly",
			rel:  commonv1.ReleaseInfo{Protocol: commonv1.ProtocolTorrent, GUID: "g", DownloadURL: "https://x/t.torrent"},
			want: commonv1.DownloadSource{TorrentURL: new("https://x/t.torrent")},
		},
		{
			name: "an nzb no indexer stands behind is fetched directly",
			rel:  commonv1.ReleaseInfo{Protocol: commonv1.ProtocolUsenet, IndexerRef: "idx", DownloadURL: "https://x/a.nzb"},
			want: commonv1.DownloadSource{NZBURL: new("https://x/a.nzb")},
		},
		{
			name: "a magnet field that is not a magnet falls through to index",
			rel: commonv1.ReleaseInfo{
				Protocol: commonv1.ProtocolTorrent, GUID: "g", IndexerRef: "idx",
				MagnetURL: "https://aggregator/redirect/123",
			},
			want: commonv1.DownloadSource{
				IndexerDownload: &commonv1.IndexerDownload{IndexerRef: "idx", GUID: "g"},
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

func members(s commonv1.DownloadSource) int {
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
