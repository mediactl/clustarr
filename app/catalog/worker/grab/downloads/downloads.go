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

// Package downloads holds the facts about a Download that both grab paths
// must agree on: which spec.source a release maps to, and which Downloads are
// working on a given catalog item.
//
// It is a leaf -- it imports only API types -- so the Search controller and
// the grab worker share it without either importing the other's package. The
// source mapping used to live in both, restated slightly differently, which is
// how an interactive grab and an automatic grab of one release came to be
// unable to coexist.
//
// Whether a Download is still live is not decided here: that is
// catalogarr/controller/rollup.DownloadNonTerminal, the set the Movie and
// Episode reconcilers derive status.activeDownloadRef from (gap-fix ruling
// R-5), which the grab path's guard uses too so the two cannot disagree.
package downloads

import (
	"encoding/base32"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// ErrNoSource reports a release with nothing a download engine could fetch it
// by: no magnet, no indexer to resolve it through, and no direct URL. It is
// permanent -- the release snapshot is immutable -- so a caller should give up
// on the release rather than retry it.
var ErrNoSource = errors.New("downloads: release has no downloadable source")

// ResolveSource maps a release to the spec.source of the Download that grabs
// it. It is the only such mapping: the grab worker (automatic search, RSS and
// delayed grabs) and the Search controller (a user's spec.grab, through
// catalogarr/controller/search.BuildDownloadSource) both call it.
//
// It has to be one function, and a function of the release snapshot alone,
// because both paths name the Download identically -- k8s.ChildName(target,
// guid) -- and DownloadSpec.Source is `self == oldSelf`. When the two paths
// mapped one release differently (one added expectedInfoHash, one could pick
// a direct URL depending on whether the Indexer had a Secret), whichever
// applied second was rejected with "source is immutable", and a delayed grab
// rejected that way dead-lettered and left its item at Phase=Delayed for
// good. Nothing read here can change between two paths' grabs of one release,
// so the two cannot disagree.
//
// The order is:
//
//  1. magnetURL, when the release carries a magnet link: an engine adds it
//     with no fetch at all.
//  2. indexerDownload, whenever the release names the Indexer and GUID it came
//     from. indexarr resolves it (rpc.indexarr.download), which applies the
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
func ResolveSource(rel commonv1.ReleaseInfo) (downloadv1alpha1.DownloadSource, error) {
	var src downloadv1alpha1.DownloadSource
	switch {
	case isMagnet(rel.MagnetURL):
		m := rel.MagnetURL
		src.MagnetURL = &m
	case rel.IndexerRef != "" && rel.GUID != "":
		src.IndexerDownload = &downloadv1alpha1.IndexerDownload{
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
		return downloadv1alpha1.DownloadSource{}, ErrNoSource
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
// treated as a magnet at all and the release falls through to indexarr.
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

// SourceApplyConfiguration converts a DownloadSource into the apply
// configuration k8s.Apply needs. It is a field-by-field copy rather than a
// marshal/unmarshal round trip so a new member added to DownloadSource fails
// to compile here instead of being silently dropped on the wire.
func SourceApplyConfiguration(src downloadv1alpha1.DownloadSource) *downloadac.DownloadSourceApplyConfiguration {
	ac := downloadac.DownloadSource()
	if src.MagnetURL != nil {
		ac = ac.WithMagnetURL(*src.MagnetURL)
	}
	if src.TorrentURL != nil {
		ac = ac.WithTorrentURL(*src.TorrentURL)
	}
	if src.NZBURL != nil {
		ac = ac.WithNZBURL(*src.NZBURL)
	}
	if src.IndexerDownload != nil {
		// url is sent even when empty, as both paths always have: source is
		// `self == oldSelf`, and an absent key does not equal an empty one,
		// so changing that now would make a re-apply onto a Download created
		// before this function existed fail as "source is immutable".
		ac = ac.WithIndexerDownload(downloadac.IndexerDownload().
			WithIndexerRef(src.IndexerDownload.IndexerRef).
			WithGUID(src.IndexerDownload.GUID).
			WithURL(src.IndexerDownload.URL))
	}
	if src.ExpectedInfoHash != nil {
		ac = ac.WithExpectedInfoHash(*src.ExpectedInfoHash)
	}
	return ac
}

// Covers reports whether dl is a grab for the catalog item of the given kind,
// name and UID -- the grab path's double-grab guard's notion of "this item
// already has a Download".
//
// It does if dl carries an ownerReference to the item's UID -- every grab
// path makes the Download's target its owner -- or if dl's spec.target names
// the item: directly, or, for an Episode, as one of the keys of a season pack
// whose target is the Series. The second arm is what makes a pack visible from
// its episodes: a pack's owner is its Series, so no Episode in it has an
// ownerReference to match, and an owner-only check would let an episode
// already inside a downloading pack be grabbed a second time.
//
// It is deliberately wider than the reconcilers' choice of which Download
// status.activeDownloadRef names (rollup.ActiveDownload, which also requires
// the owner). A Download the Search controller created while its item was
// missing has no owner, but importarr still imports it into whatever
// spec.target names, so for refusing a second grab it counts.
//
// uid may be empty when the caller has only a name; the ownerReference arm
// then never matches and spec.target decides alone.
func Covers(dl *downloadv1alpha1.Download, kind commonv1.MediaKind, name string, uid types.UID) bool {
	if uid != "" {
		for _, ref := range dl.OwnerReferences {
			if ref.UID == uid {
				return true
			}
		}
	}
	t := dl.Spec.Target
	if t.Kind == kind && t.Name == name {
		return true
	}
	return kind == commonv1.MediaKindEpisode &&
		t.Kind == commonv1.MediaKindSeries &&
		slices.Contains(t.Keys, name)
}
