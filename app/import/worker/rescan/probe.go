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

package rescan

import (
	"context"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo/ffprobeexec"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
)

// VideoProber reads a video file's technical description.
type VideoProber func(ctx context.Context, path string) (*commonv1.MediaInfo, error)

// videoProbeTimeout bounds one file's probe (probeVideo). A probe reads the
// container header and one frame, which a healthy file answers in well
// under a second, even over a network mount; one that hangs -- a stalled
// mount, a pathological file -- must not hold the walk, so running out of
// it is a probe failure like any other. It must fit inside
// ConsumerImportScan's acknowledgement deadline, which is BackOff[0] (30s),
// not its AckWait: the walk heartbeats immediately before the probe
// (fileProbe.heartbeat), and TestTheWalkFitsTheScanConsumersAckDeadline
// holds this, heartbeatInterval and defaultMetadataTimeout to that
// deadline together, so a topology change trips it.
const videoProbeTimeout = 15 * time.Second

// probeVideo is the production [VideoProber]: pkg/mediainfo's probe, the
// same reading catalogarr's probe gives the file, bounded by
// videoProbeTimeout.
func probeVideo(ctx context.Context, path string) (*commonv1.MediaInfo, error) {
	pctx, cancel := context.WithTimeout(ctx, videoProbeTimeout)
	defer cancel()
	mi, _, err := ffprobeexec.Probe(pctx, path)
	return mi, err
}

// fileProbe is one walked file's probe, run at most once however many of
// the walk's decisions read it -- the kept-output tag check (keptOutput)
// and the quality correction (attributeMediaFile), and every conflict
// retry of the latter (handleMediaFile) -- and only when one does. visit
// makes one per file.
type fileProbe struct {
	path  string
	probe VideoProber // nil: the worker probes nothing

	// heartbeat extends the delivery's ack deadline. It runs immediately
	// before the probe, so the probe's whole bound lies inside the
	// deadline however long the walk has gone since its last beat.
	heartbeat func(ctx context.Context) error

	ran bool
	mi  *commonv1.MediaInfo
	err error
}

// probeFor is path's fileProbe, through [Worker.ProbeVideo], heartbeating
// through heartbeat.
func (w *Worker) probeFor(path string, heartbeat func(ctx context.Context) error) *fileProbe {
	return &fileProbe{path: path, probe: w.ProbeVideo, heartbeat: heartbeat}
}

// available reports whether the worker probes at all.
func (p *fileProbe) available() bool { return p.probe != nil }

// result heartbeats and probes the file the first time it is asked, and
// returns that one result every time after. The probe's own failure is
// probeErr, which each caller decides on. A failed heartbeat is fatal: it
// ends the walk, as a failure of the walk's own heartbeat does, rather than
// reading as a file that could not be probed. Callers check available
// first.
func (p *fileProbe) result(ctx context.Context) (mi *commonv1.MediaInfo, probeErr, fatal error) {
	if !p.ran {
		if p.heartbeat != nil {
			if err := p.heartbeat(ctx); err != nil {
				return nil, nil, err
			}
		}
		p.mi, p.err = p.probe(ctx, p.path)
		p.ran = true
	}
	return p.mi, p.err, nil
}

// probedQuality corrects a name-derived quality from the file's probe
// (quality.AugmentFromMediaInfo): the resolution the stream really has,
// onto the quality ladder, under the name's source. A probe failure never
// fails the scan -- it is logged and the file keeps the quality its name
// says, as it did before scans probed -- and neither does a worker that
// probes nothing. The error is a failed heartbeat's (fileProbe.result).
func probedQuality(ctx context.Context, probe *fileProbe, rel string, q commonv1.Quality) (commonv1.Quality, error) {
	if !probe.available() {
		return q, nil
	}
	mi, perr, err := probe.result(ctx)
	if err != nil {
		return q, err
	}
	if perr != nil {
		logging.FromContext(ctx).Warn("rescan: could not probe the file; recording it under its name-derived quality",
			"path", rel, "error", perr)
		return q, nil
	}
	q, _ = quality.AugmentFromMediaInfo(q, mi)
	return q, nil
}
