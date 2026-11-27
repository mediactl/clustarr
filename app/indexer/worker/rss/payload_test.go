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

package rss_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/grabsource"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// An indexer that mirrors torrents to Usenet lists every release in one
// feed: the release's page as <link>, a torrent enclosure, an NZB one once
// it is mirrored, and the torrent's magnet, info hash and swarm as attrs.
// The fixture is two items of the Anime Tosho successor's answer
// (feed.animetosho.net, 2026-11-27): one mirrored, one a torrent only.
// Read as it was, a usenet indexer's release was fetched by its magnet
// (grabsource.ResolveSource takes a magnet first), and without the magnet
// its "NZB" was the release's page.
func TestAMirrorsFeedIsReadForTheIndexersOwnProtocol(t *testing.T) {
	f, err := os.Open("../../../../test/data/torznab/mirror_both_protocols.xml")
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	raw, err := torznab.ParseResults(f)
	require.NoError(t, err)
	require.Len(t, raw, 2)
	mirrored, torrentOnly := raw[0], raw[1]

	t.Run("a usenet indexer takes the NZB, and nothing of the torrent", func(t *testing.T) {
		require.False(t, rss.NoPayload(mirrored, "usenet"))
		info := rss.ProjectRelease(mirrored, "animetosho", "usenet").Info
		assert.Equal(t, "https://feed.animetosho.net/nzb/687355", info.DownloadURL)
		assert.Empty(t, info.MagnetURL)
		assert.Empty(t, info.InfoHash)
		assert.Nil(t, info.Seeders)
		assert.Nil(t, info.Leechers)
		assert.Equal(t, int64(8423228390), info.SizeBytes)

		src, err := grabsource.ResolveSource(info)
		require.NoError(t, err)
		assert.Nil(t, src.MagnetURL, "a usenet release is never fetched by a magnet")
		require.NotNil(t, src.IndexerDownload)
		assert.Equal(t, "https://feed.animetosho.net/nzb/687355", src.IndexerDownload.URL)
	})
	t.Run("a release with no NZB is not a usenet indexer's release", func(t *testing.T) {
		assert.True(t, rss.NoPayload(torrentOnly, "usenet"))
	})
	t.Run("a torrent indexer on the same feed takes the torrent of both", func(t *testing.T) {
		for _, r := range raw {
			require.False(t, rss.NoPayload(r, "torrent"))
			info := rss.ProjectRelease(r, "animetosho", "torrent").Info
			assert.Contains(t, info.DownloadURL, ".torrent")
			assert.NotEmpty(t, info.MagnetURL)
			assert.NotEmpty(t, info.InfoHash)
		}
	})
}

// A plain feed is read as it always was: an item whose one enclosure is its
// protocol's keeps it, and an item whose enclosure names no type keeps its
// <link>.
func TestAPlainFeedKeepsItsLink(t *testing.T) {
	typed := torznab.Release{
		Link:       "https://indexer.example.invalid/get/1",
		Enclosures: []torznab.Enclosure{{URL: "https://indexer.example.invalid/get/1", Type: "application/x-nzb"}},
	}
	untyped := torznab.Release{
		Link:       "https://indexer.example.invalid/details/2",
		Enclosures: []torznab.Enclosure{{URL: "https://indexer.example.invalid/get/2"}},
	}
	jackett := torznab.Release{
		Link:       "https://jackett.example.invalid/dl/3",
		Enclosures: []torznab.Enclosure{{URL: "https://jackett.example.invalid/dl/3", Type: "application/x-bittorrent;x-scheme-handler/magnet"}},
	}
	magnetOnly := torznab.Release{
		MagnetURL:  "magnet:?xt=urn:btih:05bd3a45348e1338117e89cf1e25fd76b7a30de3",
		Enclosures: []torznab.Enclosure{{URL: "https://indexer.example.invalid/nzb/4", Type: "application/x-nzb"}},
	}
	for _, tc := range []struct {
		name, protocol, want string
		r                    torznab.Release
	}{
		{"a typed NZB enclosure", "usenet", "https://indexer.example.invalid/get/1", typed},
		{"an enclosure with no type", "usenet", "https://indexer.example.invalid/details/2", untyped},
		{"a Jackett enclosure with a parameter", "torrent", "https://jackett.example.invalid/dl/3", jackett},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.False(t, rss.NoPayload(tc.r, tc.protocol))
			assert.Equal(t, tc.want, rss.ProjectRelease(tc.r, "idx", tc.protocol).Info.DownloadURL)
		})
	}
	assert.False(t, rss.NoPayload(magnetOnly, "torrent"), "a torrent is one by its magnet alone")
	assert.False(t, rss.NoPayload(torznab.Release{Link: "https://indexer.example.invalid/get/5"}, "usenet"))
	assert.Equal(t, commonv1.ProtocolUsenet, rss.ProjectRelease(typed, "idx", "usenet").Info.Protocol)
}
