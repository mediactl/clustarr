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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/delay"
)

// Bypasses is delay.Bypasses (moved there, ADR-0019 A4.1).
func Bypasses(spec catalogv1alpha1.DelayProfileSpec, atTopTier bool, formatScore int32) bool {
	return delay.Bypasses(spec, atTopTier, formatScore)
}

// DelayFor is delay.DelayFor.
func DelayFor(spec catalogv1alpha1.DelayProfileSpec, protocol commonv1.Protocol) time.Duration {
	return delay.DelayFor(spec, protocol)
}

// ProtocolEnabled is delay.ProtocolEnabled.
func ProtocolEnabled(spec catalogv1alpha1.DelayProfileSpec, protocol commonv1.Protocol) bool {
	return delay.ProtocolEnabled(spec, protocol)
}
