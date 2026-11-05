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
	"fmt"
	"math"
	"strconv"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"

	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// Plex's video conversions share the Intel GPU with clustarr's batch
// encodes, and a viewer's stream comes first (owner, 2026-10-26;
// cluster-plex's docs/superpowers/specs/2026-10-26-gpu-video-transcode-pool-design.md
// §6). cluster-plex counts the conversions its GPU pool runs in a Lease it
// alone writes; while that count is fresh and above zero, admission admits
// new files to the intel class only on the video engines the streams
// leave whole (VideoAllowance; owner, 2026-11-05: "maximum transcode
// streams without throttling the streams"), and the encodes already
// running finish.
const (
	// VideoTranscodesAnnotation is the Lease's count.
	VideoTranscodesAnnotation = "clusterplex.io/active-video-transcodes"
	// videoLeaseStale is how long a count holds after its renewal: a
	// cluster-plex manager renews every 10 s while it converts.
	videoLeaseStale = 30 * time.Second
	// YieldingToPlex is the wait reason of a file held for Plex's streams.
	YieldingToPlex = "waiting: Plex is converting video on the GPU"
	// videoStreamShare is the share of one video engine a Live TV
	// conversion is reserved. Measured 2026-11-05 on talos-88's Arc 140T
	// (two video engines): a 1080p H.264 stream converted to H.264 at real
	// time held 20% of one engine, in bursts, with the GPU asleep between
	// them; a quarter leaves a margin for a heavier stream. A batch encode
	// held one engine 97% busy (2026-11-03), so it is a whole engine.
	// Measured again on 2026-11-05 (a benchmark pod, N parallel jobs of
	// one episode): one encode spreads over both engines (149% of 200%),
	// two fill them (195%) and throughput saturates there (22, 29, 30 MB/s
	// of source at 1, 2, 3 jobs), so the whole-engine reservation is what
	// is left to a stream, not how the hardware schedules; two encodes
	// beside a stream would give it about a third of an engine, above its
	// 20%, which is untested for a real-time stream.
	videoStreamShare = 0.25
)

// VideoAllowance is how many encodes may start beside streams conversions
// on a device with engines video engines: the streams' engines are
// reserved whole (each stream videoStreamShare of one, rounded up, at
// least one engine while any stream runs), so a stream never shares an
// engine with an encode, and every engine left takes one encode. An
// unmeasured count (0: an older worker, or a driver that reports none) is
// one engine, which yields the whole device while any stream runs, as
// before the count. On the Arc 140T: one encode beside one to four
// streams, none beside five or more; without streams the pool's own
// parallelism decides.
func VideoAllowance(engines, streams int) int {
	engines = max(engines, 1)
	if streams <= 0 {
		return engines
	}
	reserved := max(int(math.Ceil(float64(streams)*videoStreamShare)), 1)
	return max(engines-reserved, 0)
}

// videoEngines is the measured engine count of a class's device, from the
// limits every tier of the class shares; 0 when no report carries one.
func videoEngines(d transcodeplan.Device) int {
	n := 0
	for _, l := range d.Limits {
		n = max(n, l.VideoEngines)
	}
	return n
}

// yieldMessage is the wait reason of a file held for Plex's streams: the
// bare YieldingToPlex when no encode may run beside them, else how many do.
func yieldMessage(streams, beside int) string {
	if beside <= 0 {
		return YieldingToPlex
	}
	return fmt.Sprintf("%s (%d stream(s); %d encode(s) run beside them)", YieldingToPlex, streams, beside)
}

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
