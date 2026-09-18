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

package decision

import (
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// protocolRejection is ProtocolSpecification: a protocol absent from
// o.ProtocolsEnabled is treated as disabled. The caller populates both
// "torrent" and "usenet" from the resolved DelayProfile's
// EnableUsenet/EnableTorrent (default true on the CRD), so an absent key in
// practice means "no DelayProfile resolved yet," which should fail closed.
func protocolRejection(rel common.ReleaseInfo, o Options) *common.Rejection {
	if o.ProtocolsEnabled[string(rel.Protocol)] {
		return nil
	}
	r := newRejection(ReasonProtocolDisabled, "%s is not enabled", rel.Protocol)
	return &r
}

// availabilityRejection is RssSync/AvailabilitySpecification: skipped
// entirely for a user-invoked (interactive) search.
func availabilityRejection(t Target, o Options) *common.Rejection {
	if o.UserInvoked || t.Available {
		return nil
	}
	r := newRejection(ReasonUnavailable, "item is not yet available")
	return &r
}
