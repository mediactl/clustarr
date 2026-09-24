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

package fileimport

import (
	"cmp"
	"fmt"
	"os"
	"slices"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// One file per item, the best one.
//
// A download often holds more than one file for the same item: an ebook
// release ships EPUB, MOBI and AZW3; a movie release a second cut or a
// re-encode; a season pack a PROPER beside the original. The item holds one
// file (a movie, a book, an issue, an episode), so importing each in walk
// order made one MediaFile per file for one item -- two files backing one
// Book, the later one never compared with the earlier. Radarr's
// ImportApprovedMovie and Sonarr's ImportApprovedEpisodes instead decide
// every file first, order the approved ones by quality (the profile's rank,
// then the proper/repack revision) and then size, and import down that
// order, rejecting a later file whose item this import already filled
// ("Movie has already been imported"). The walks below do the same: admit
// every file, rank the candidates, then import best first.

// fileCandidate is one file a walk admitted for import, with the quality it
// was ranked by (known false when none could be determined).
type fileCandidate struct {
	path    string
	info    os.FileInfo
	quality commonv1.Quality
	known   bool
	rank    candidateRank
}

// ranked sets c's quality and rank against profile.
func (c *fileCandidate) ranked(profile quality.Profile, q commonv1.Quality, rev commonv1.Revision, known bool) {
	c.quality, c.known = q, known
	c.rank = rankOf(profile, q, rev, known, c.info.Size())
}

// lateRejection is the rejection for a candidate ranked after the file that
// filled its item. A file the profile does not allow keeps that reason, as
// a rejected decision keeps its own in Radarr: only a file that would have
// been imported is told the item is filled.
func (c fileCandidate) lateRejection(rel, item, by string) string {
	if c.known && !c.rank.allowed {
		return notAllowedRejection(rel, c.quality)
	}
	return filledRejection(rel, item, by)
}

// notAllowedRejection is the rejection for a file whose quality the profile
// does not allow.
func notAllowedRejection(rel string, q commonv1.Quality) string {
	return fmt.Sprintf("%s: quality %s is not allowed by the quality profile", rel, q.Name)
}

// candidateRank orders candidates for one item, best first: a quality the
// profile allows before one it does not (or cannot place), then the
// profile's own tier order, then the higher revision, then the larger file.
type candidateRank struct {
	allowed bool
	tier    int
	version int32
	real    int32
	size    int64
}

// rankOf ranks a file whose quality is q (known is false when it could not
// be determined) of size bytes against profile.
func rankOf(profile quality.Profile, q commonv1.Quality, rev commonv1.Revision, known bool, size int64) candidateRank {
	r := candidateRank{version: rev.Version, real: rev.Real, size: size}
	if known {
		if idx, ok := profile.Index(q); ok && profile.Allowed(q) {
			r.allowed, r.tier = true, idx
		}
	}
	return r
}

// compareRank is a slices.SortStableFunc comparison: negative when a ranks
// before (is better than) b.
func compareRank(a, b candidateRank) int {
	switch {
	case a.allowed != b.allowed:
		if a.allowed {
			return -1
		}
		return 1
	case a.tier != b.tier:
		return cmp.Compare(a.tier, b.tier) // tiers are listed best first
	case a.version != b.version:
		return cmp.Compare(b.version, a.version)
	case a.real != b.real:
		return cmp.Compare(b.real, a.real)
	default:
		return cmp.Compare(b.size, a.size)
	}
}

// sortCandidates orders cs best first; equal candidates keep walk order.
func sortCandidates(cs []fileCandidate) {
	slices.SortStableFunc(cs, func(a, b fileCandidate) int { return compareRank(a.rank, b.rank) })
}

// filledRejection is the rejection for a candidate whose item a better file
// of the same download already filled.
func filledRejection(rel, item, by string) string {
	return rel + ": " + item + " already has a file from this download, " + by +
		", which ranks higher (quality, then revision, then size)"
}
