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

package mediainfo

import (
	"context"
	"errors"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Prober reads a media file: Probe its technical description and the Raw
// fields only transcode planning reads, ProbeAudio one audio stream's codec,
// bitrate and sample size. The import domain holds one per process and hands
// it to the probe worker (app/import/worker/probe). It is ffprobeexec.Prober
// until the in-process ffgo probe lands (spec 2026-10-06 §6.2, §6.9).
type Prober interface {
	Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *Raw, error)
	ProbeAudio(ctx context.Context, path string) (AudioProbe, error)
}

// ErrProbeAbandoned is a probe that outlived its deadline and the grace after
// it: the call returned, but the work it started may still be running. A
// Prober wraps it. The probe worker records such a probe as a non-transient
// failure and counts it (schema.ProbeRecord.AbandonedCount), so a file that
// wedges the prober is retried hourly and then given up on, rather than every
// five minutes (spec §6.6). It is declared here, not beside the FFmpeg runtime
// that abandons the call, so the probe worker can test for it without linking
// FFmpeg.
var ErrProbeAbandoned = errors.New("mediainfo: the probe outlived its deadline and was abandoned")

// AtPath is mi as a probe of the same bytes at path records it: a deep copy
// with Container read from path, the one field toMediaInfo takes from the file
// name. A nil mi is nil.
func AtPath(mi *commonv1.MediaInfo, path string) *commonv1.MediaInfo {
	if mi == nil {
		return nil
	}
	out := mi.DeepCopy()
	out.Container = containerFromPath(path)
	return out
}
