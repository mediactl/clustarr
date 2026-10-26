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

package admission

import (
	"strconv"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
)

// Plex's video conversions share the Intel GPU with clustarr's batch
// encodes, and a viewer's stream comes first (owner, 2026-10-26;
// cluster-plex's docs/superpowers/specs/2026-10-26-gpu-video-transcode-pool-design.md
// §6). cluster-plex counts the conversions its GPU pool runs in a Lease it
// alone writes; while that count is fresh and above zero, admission admits
// no new file to the intel class, and the encodes already running finish.
const (
	// VideoTranscodesAnnotation is the Lease's count.
	VideoTranscodesAnnotation = "clusterplex.io/active-video-transcodes"
	// videoLeaseStale is how long a count holds after its renewal: a
	// cluster-plex manager renews every 10 s while it converts.
	videoLeaseStale = 30 * time.Second
	// YieldingToPlex is the wait reason of a file held for Plex's streams.
	YieldingToPlex = "waiting: Plex is converting video on the GPU"
)

// videoStreams is the Lease's count while it was renewed within
// videoLeaseStale, else 0 -- cluster-plex's lease.Count, read the same way.
func videoStreams(l *coordinationv1.Lease, now time.Time) int {
	if l == nil || l.Spec.RenewTime == nil || now.Sub(l.Spec.RenewTime.Time) >= videoLeaseStale {
		return 0
	}
	n, err := strconv.Atoi(l.Annotations[VideoTranscodesAnnotation])
	if err != nil || n < 0 {
		return 0
	}
	return n
}
