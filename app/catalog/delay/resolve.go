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

// Package delay resolves which DelayProfile governs a grab (spec §8.2). It is
// pure -- profiles in, one profile out -- so the grab sink's resolver and
// the RSS matcher share it without linking the DelayProfile controller
// (design 2026-10-06 §4.3 C9).
package delay

import (
	"errors"
	"slices"
	"time"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

var (
	ErrNoProfiles = errors.New("delayprofile: no profiles given")
	ErrNoMatch    = errors.New("delayprofile: no profile matches and no catch-all is configured")
)

// Resolve implements spec §8.2's delay-profile resolution order: item ref ->
// tag match -> lowest order.
//
//  1. If ref names a profile present in profiles, that profile wins
//     outright, no matter its Tags or Order. A ref naming an absent profile
//     is treated as no ref (an inferred rule -- the spec does not say what a
//     dangling ref does -- chosen so a deleted DelayProfile cannot wedge
//     every item that referenced it).
//  2. Otherwise, the candidate set is every profile whose Tags intersects
//     itemTags, plus every profile whose Tags is empty (a catch-all, "the
//     chart installs `default` with order 1000 and no tags"). The candidate
//     with the lowest Order wins; ties break by Name for determinism. Tag
//     specificity is not a tie-breaker: Order alone decides once a profile
//     is a candidate, matching the CRD field doc's literal "lowest order"
//     rule.
//  3. An empty candidate set (no tag match and no catch-all configured) is
//     ErrNoMatch. An empty profiles slice is ErrNoProfiles.
func Resolve(ref *string, itemTags []string, profiles []catalogv1alpha1.DelayProfile) (*catalogv1alpha1.DelayProfile, error) {
	if len(profiles) == 0 {
		return nil, ErrNoProfiles
	}

	if ref != nil && *ref != "" {
		for i := range profiles {
			if profiles[i].Name == *ref {
				return &profiles[i], nil
			}
		}
		// dangling ref: fall through to tag match.
	}

	var best *catalogv1alpha1.DelayProfile
	for i := range profiles {
		p := &profiles[i]
		if len(p.Spec.Tags) > 0 && !tagsIntersect(p.Spec.Tags, itemTags) {
			continue
		}
		if best == nil || p.Spec.Order < best.Spec.Order ||
			(p.Spec.Order == best.Spec.Order && p.Name < best.Name) {
			best = p
		}
	}
	if best == nil {
		return nil, ErrNoMatch
	}
	return best, nil
}

func tagsIntersect(a, b []string) bool {
	for _, t := range a {
		if slices.Contains(b, t) {
			return true
		}
	}
	return false
}

// Bypasses reports whether a DelayProfile's delay is skipped for this
// release, per spec §8.2's two bypasses: "BypassIfHighestQuality and quality
// index == top tier; or BypassIfAboveFormatScore and score >= min" (P15,
// moved here from the retired grab worker, ADR-0019 A4.1).
//
// Both flags are *bool on the CRD with a +kubebuilder:default, so a spec read
// back from the apiserver always has them set -- but a spec built in memory
// (a test, a defaulted-but-not-yet-persisted object) does not, and a nil there
// must mean the documented default, not false. ptr.Deref supplies it.
func Bypasses(spec catalogv1alpha1.DelayProfileSpec, atTopTier bool, formatScore int32) bool {
	if ptr.Deref(spec.BypassIfHighestQuality, true) && atTopTier {
		return true
	}
	return ptr.Deref(spec.BypassIfAboveFormatScore, false) && formatScore >= spec.MinimumFormatScore
}

// DelayFor is the delay this profile imposes on a release of the given
// protocol. An unrecognised protocol delays nothing: a release Clustarr cannot
// classify must not be held hostage by a profile that never mentions it.
func DelayFor(spec catalogv1alpha1.DelayProfileSpec, protocol commonv1.Protocol) time.Duration {
	switch protocol {
	case commonv1.ProtocolUsenet:
		return time.Duration(spec.UsenetDelayMinutes) * time.Minute
	case commonv1.ProtocolTorrent:
		return time.Duration(spec.TorrentDelayMinutes) * time.Minute
	default:
		return 0
	}
}

// ProtocolEnabled reports whether the profile allows this protocol at all.
// Both flags default to true on the CRD, so the nil case is "allowed".
func ProtocolEnabled(spec catalogv1alpha1.DelayProfileSpec, protocol commonv1.Protocol) bool {
	switch protocol {
	case commonv1.ProtocolUsenet:
		return ptr.Deref(spec.EnableUsenet, true)
	case commonv1.ProtocolTorrent:
		return ptr.Deref(spec.EnableTorrent, true)
	default:
		return false
	}
}
