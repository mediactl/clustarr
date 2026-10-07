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

package grabplan

import (
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/delay"
)

// The delay profile's rules (P15), app/catalog/delay's: the delay a
// protocol takes, its two bypasses (the top tier, a format score at or
// above the minimum) and whether the profile allows the protocol at all.
// The profile itself is resolved by the stage (delay.Resolve over the
// namespace's DelayProfiles and the owner's tags).

func delayFor(spec catalogv1alpha1.DelayProfileSpec, p commonv1.Protocol) time.Duration {
	return delay.DelayFor(spec, p)
}

func bypasses(spec catalogv1alpha1.DelayProfileSpec, atTopTier bool, formatScore int32) bool {
	return delay.Bypasses(spec, atTopTier, formatScore)
}

func protocolEnabled(spec catalogv1alpha1.DelayProfileSpec, p commonv1.Protocol) bool {
	return delay.ProtocolEnabled(spec, p)
}
