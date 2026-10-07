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

// Package grabsource maps a release to how an engine fetches it: the one
// mapping every grab uses (ADR-0019 §6.6: the owner key's grab planner,
// whose candidates come from a search, the RSS matcher or a Search's pick).
//
// It is a leaf -- it imports only API types -- so the manager's grab planner
// links nothing else for it. The mapping used to live in the grab worker and
// the Search controller both, restated slightly differently, which is how an
// interactive grab and an automatic grab of one release came to be unable to
// coexist.
package grabsource

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// ErrNoSource reports a release with nothing a download engine could fetch it
// by: no magnet, no indexer to resolve it through, and no direct URL. It is
// permanent -- the release snapshot is immutable -- so a caller should give up
// on the release rather than retry it.
var ErrNoSource = errors.New("downloads: release has no downloadable source")

// ResolveSource maps a release to the source of the grab entry that fetches
// it (ADR-0019 §6.2). It is a function of the release snapshot alone, so a
// delayed grab made from status, an RSS match and a pick of one release all
// fetch it the same way.
//
// The order is:
//
//  1. magnetURL, when the release carries a magnet link: an engine adds it
//     with no fetch at all.
//  2. indexerDownload, whenever the release names the Indexer and GUID it came
//     from. index resolves it (rpc.index.download), which applies the
//     indexer's credentials, rate limit and proxy and counts the grab in
//     status.grabsInWindow. Spec §8.2 requires this "when the indexer is
//     authenticated"; it is used for every indexer because whether an
//     Indexer is authenticated is not a property of the release -- a Secret
//     can be added after the decision -- and because a direct fetch would
//     bypass the Indexer's proxyRef and its grab accounting. A release's
//     downloadURL is also usually the indexer's own link with its API key in
//     the query string, which does not belong in a Download object.
//  3. torrentURL or nzbURL, only for a release no Indexer stands behind (no
//     indexerRef or no GUID), so there is nothing to resolve it through.
//
// ExpectedInfoHash is set on a torrent whenever the release carries an info
// hash, on every branch: it is what stops an indexer swapping content out from
// under a grab decision, and it is orthogonal to how the payload is
// addressed. See expectedInfoHash for the forms accepted.
func ResolveSource(rel commonv1.ReleaseInfo) (commonv1.DownloadSource, error) {
	var src commonv1.DownloadSource
	switch {
	case isMagnet(rel.MagnetURL):
		m := rel.MagnetURL
		src.MagnetURL = &m
	case rel.IndexerRef != "" && rel.GUID != "":
		src.IndexerDownload = &commonv1.IndexerDownload{
			IndexerRef: rel.IndexerRef,
			GUID:       rel.GUID,
			URL:        rel.DownloadURL,
		}
	case rel.DownloadURL != "" && rel.Protocol == commonv1.ProtocolTorrent:
		u := rel.DownloadURL
		src.TorrentURL = &u
	case rel.DownloadURL != "" && rel.Protocol == commonv1.ProtocolUsenet:
		u := rel.DownloadURL
		src.NZBURL = &u
	default:
		return commonv1.DownloadSource{}, ErrNoSource
	}
	if rel.Protocol == commonv1.ProtocolTorrent {
		if h, ok := expectedInfoHash(rel.InfoHash); ok {
			src.ExpectedInfoHash = &h
		}
	}
	return src, nil
}

// isMagnet mirrors DownloadSource.MagnetURL's CRD pattern, ^magnet:\?.+$. A
// "magnet" field holding anything else -- some aggregators put their own
// redirect link there -- would be rejected by the apiserver, so it is not
// treated as a magnet at all and the release falls through to index.
func isMagnet(s string) bool {
	return strings.HasPrefix(s, "magnet:?") && len(s) > len("magnet:?")
}

// expectedInfoHash normalizes an info hash to the form
// DownloadSource.ExpectedInfoHash's CRD pattern accepts,
// ^[0-9a-f]{40}([0-9a-f]{24})?$: lower-case hex, 40 characters for BitTorrent
// v1 or 64 for v2. Indexers report the v1 hash in upper case often enough, and
// in the 32-character base32 form magnet links also allow, that passing it
// through verbatim got the whole Download rejected. Anything that is not one
// of those forms is left off rather than failing the grab: the guard is lost
// for that release, which is strictly better than losing the release.
func expectedInfoHash(h string) (string, bool) {
	h = strings.TrimSpace(h)
	switch len(h) {
	case 40, 64:
		lower := strings.ToLower(h)
		if _, err := hex.DecodeString(lower); err != nil {
			return "", false
		}
		return lower, true
	case 32:
		raw, err := base32.StdEncoding.DecodeString(strings.ToUpper(h))
		if err != nil || len(raw) != 20 {
			return "", false
		}
		return hex.EncodeToString(raw), true
	default:
		return "", false
	}
}
