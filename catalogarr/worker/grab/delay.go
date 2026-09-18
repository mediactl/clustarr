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
	"time"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Bypasses reports whether a DelayProfile's delay is skipped for this
// release, per spec §8.2's two bypasses: "BypassIfHighestQuality and quality
// index == top tier; or BypassIfAboveFormatScore and score >= min".
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
//
// It is not part of the delay arithmetic -- Decide does not call it -- but it
// is the DelayProfile field pkg/decision.Options.ProtocolsEnabled is populated
// from, and callers that resolve a profile through this package should not
// have to restate the nil-means-true rule.
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
