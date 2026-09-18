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
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"slices"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// ErrUnsupportedKind is returned for a media kind this package cannot hold a
// delayed grab for. M1's scope is movie plus episode/series: those are the
// only kinds whose status carries a PendingGrab field, so a comic Issue or an
// Album has nowhere to record "chosen, waiting out a delay" and is rejected
// rather than silently grabbed without its delay profile.
var ErrUnsupportedKind = errors.New("grab: unsupported media kind")

// MediaKey is the <mediaKey> token for one catalog item: a thin adapter over
// events.MediaKey, which is the canonical builder and the only place the
// token's shape is defined.
//
// It exists so call sites read in the vocabulary they already hold -- a
// namespace and a commonv1.MediaRef -- without pkg/events having to import the
// API types. Never build the token by hand: events.MediaKey's doc comment
// explains why the digest suffix is load-bearing, and Task C8 publishes the
// GrabTasks this package consumes with the same builder, so a hand-rolled
// token is a silent routing bug.
func MediaKey(namespace string, ref commonv1.MediaRef) string {
	return events.MediaKey(string(ref.Kind), namespace, ref.Name)
}

// MediaKeyFor is the <mediaKey> token identifying one GRAB, which for a pack
// is not the same thing as the token identifying its target.
//
// A movie or a single episode is its own grab, so this is MediaKey. A pack is
// not: events.MediaKey takes only kind, namespace and name, so an S01 pack and
// an S02 pack of one Series produce the same token -- and therefore the same
// clustarr-pending key and the same Msg-Id. They would then be compared
// against each other by casKeepBest on quality alone, the loser silently
// evicted, its scheduled delivery deduplicated away, and its episodes left
// holding a status.pendingGrab that nothing would ever clear.
//
// Folding a digest of the covered episode names into the name given to
// events.MediaKey separates them. The keys are sorted first so the same pack
// described in a different order is the same grab. Lease keys are unaffected
// and stay per-episode: two packs of different seasons SHOULD contend for
// nothing, while two packs overlapping one episode SHOULD still contend for
// that episode's lease.
func MediaKeyFor(namespace string, target commonv1.MediaRef, keys []string) string {
	if len(keys) == 0 {
		return MediaKey(namespace, target)
	}
	sorted := slices.Clone(keys)
	slices.Sort(sorted)
	sum := sha1.Sum([]byte(strings.Join(sorted, "\x00")))
	return events.MediaKey(string(target.Kind), namespace,
		target.Name+"."+hex.EncodeToString(sum[:])[:packKeyHashLen])
}

// packKeyHashLen matches events.MediaKey's own digest width: ten hex
// characters, 40 bits.
const packKeyHashLen = 10

// StatusTargets expands a grab target into the catalog objects whose status
// this grab touches.
//
// A movie or a single episode is its own status target. A season (or
// multi-season) pack names the Series as its target and narrows to specific
// episodes through keys, and every one of those episodes gets its own lease,
// its own pendingGrab and its own activeDownloadRef -- the Series itself has
// none of those fields, so it is never a status target in its own right.
//
// A Series target with no keys is rejected: spec §8.2's lease step is
// "for every key (episodes of a pack)", and a pack with no keys would take no
// leases at all, defeating the double-grab guard entirely.
func StatusTargets(target commonv1.MediaRef, keys []string) ([]commonv1.MediaRef, error) {
	switch target.Kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
		if len(keys) != 0 {
			return nil, ErrUnsupportedKind
		}
		return []commonv1.MediaRef{{Kind: target.Kind, Name: target.Name}}, nil
	case commonv1.MediaKindSeries:
		if len(keys) == 0 {
			return nil, ErrUnsupportedKind
		}
		out := make([]commonv1.MediaRef, 0, len(keys))
		for _, k := range keys {
			out = append(out, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: k})
		}
		return out, nil
	default:
		return nil, ErrUnsupportedKind
	}
}

// leaseKeys maps status targets onto their clustarr-leases keys.
func leaseKeys(namespace string, targets []commonv1.MediaRef) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, events.LeaseKey(MediaKey(namespace, t)))
	}
	return out
}
