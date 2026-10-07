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

// Package ffprobeexec is the probe that runs the ffprobe executable: two calls
// for a video file (go-ffprobe for the container, streams and chapters, then
// an exec'd frame probe for the first frame's colour tags and HDR side data)
// and one for an audio file. It is transitional (spec §4.2.5). pkg/mediainfo
// keeps the model and the mapping and runs nothing. The R2 step replaces this
// package with pkg/mediainfo/native and moves it to test/ffprobeoracle as the
// parity oracle.
package ffprobeexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// frameProbe is the second ffprobe call; a variable so a test can make it
// fail.
var frameProbe = runFrameProbe

// Probe runs ffprobe twice against path -- once for the container,
// streams and chapters, once for the first decoded frame's colour tags
// and HDR side data (docs/research/transcode.md §2.1) -- and returns
// both the api/common/v1alpha1 MediaInfo the MediaFile status carries
// and the Raw detail pkg/transcode needs.
func Probe(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error) {
	ctx, span := tracing.Start(ctx, "mediainfo.Probe")
	defer span.End()

	pd, err := ffprobe.ProbeURL(ctx, path)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, fmt.Errorf("mediainfo: probe %s: %w", path, err)
	}
	raw := mediainfo.BuildRaw(pd)

	if pd.FirstVideoStream() != nil {
		frames, ferr := frameProbe(ctx, path)
		if ferr == nil && len(frames.Frames) == 0 {
			ferr = errors.New("mediainfo: ffprobe frame probe decoded no frame")
		}
		if ferr != nil {
			raw.FrameErr = ferr
			// A stream that says it may be HDR cannot be read as SDR
			// without its frame: the caller retries. Any other stream
			// classifies as SDR either way, so its probe stands.
			if err := mediainfo.IncompleteHDR(raw); err != nil {
				tracing.RecordError(span, err)
				return nil, nil, fmt.Errorf("mediainfo: probe %s: %w", path, err)
			}
			logging.FromContext(ctx).WarnContext(ctx,
				"mediainfo: frame probe failed; the stream says nothing of HDR, so the file reads as SDR",
				"path", path, "error", ferr)
		} else {
			mediainfo.MergeFrame(raw, frames)
		}
	}

	return mediainfo.FromRaw(raw), raw, nil
}

// ProbeAudio runs one ffprobe call against path and returns its first audio
// stream's [mediainfo.AudioProbe]. Unlike [Probe] it makes no frame call: an
// audio file's quality needs the stream header only.
func ProbeAudio(ctx context.Context, path string) (mediainfo.AudioProbe, error) {
	ctx, span := tracing.Start(ctx, "mediainfo.ProbeAudio")
	defer span.End()

	pd, err := ffprobe.ProbeURL(ctx, path)
	if err != nil {
		tracing.RecordError(span, err)
		return mediainfo.AudioProbe{}, fmt.Errorf("mediainfo: probe %s: %w", path, err)
	}
	ap, err := mediainfo.AudioProbeFrom(pd)
	if err != nil {
		return mediainfo.AudioProbe{}, fmt.Errorf("mediainfo: probe %s: %w", path, err)
	}
	return ap, nil
}

// runFrameProbe issues the second ffprobe call: the first decoded
// frame's colour tags and HDR side data, per docs/research/transcode.md
// §2.1's second command, verbatim. go-ffprobe.v2 cannot express this
// call (no Frames field on ProbeData), so it bypasses the library.
func runFrameProbe(ctx context.Context, path string) (mediainfo.FrameProbeData, error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error", "-print_format", "json",
		"-select_streams", "v:0", "-show_frames", "-read_intervals", "%+#1",
		"-show_entries", "frame=pix_fmt,color_primaries,color_transfer,color_space,color_range,side_data_list",
		path)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return mediainfo.FrameProbeData{}, fmt.Errorf("mediainfo: ffprobe frame probe: %w: %s", err, stderr.String())
	}
	var fd mediainfo.FrameProbeData
	if err := json.Unmarshal(out.Bytes(), &fd); err != nil {
		return mediainfo.FrameProbeData{}, fmt.Errorf("mediainfo: parse frame probe json: %w", err)
	}
	return fd, nil
}
