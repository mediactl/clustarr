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

package transcode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// ArgsHash is the sha256 of plan's full rendered argv (Args, NUL-joined),
// hex-encoded: two plans that would run the same ffmpeg command have the
// same hash. The TranscodeJob controller records it as status.plan.argsHash,
// and the worker compares the argv it is about to run against it.
func ArgsHash(plan *PlanResult) string {
	sum := sha256.Sum256([]byte(strings.Join(Args(plan), "\x00")))
	return hex.EncodeToString(sum[:])
}

// Args renders plan into the exact, deterministic ffmpeg argv (global
// flags, HWInit, -i, Maps, Filters, VideoArgs, one -c:a:N/-metadata:s:a:N
// block per Audio entry, subtitle/attachment codec flags, the
// CLUSTARR_PROFILE tag, -movflags for mp4, -f <container> <Output>). A
// Skip or Reject decision returns nil (no ffmpeg invocation).
func Args(plan *PlanResult) []string {
	if plan == nil || plan.Decision == DecisionSkip || plan.Decision == DecisionReject {
		return nil
	}

	args := []string{
		"-hide_banner", "-nostdin", "-y", "-nostats",
		"-loglevel", "error",
		"-progress", "pipe:1",
		"-stats_period", "1",
	}
	args = append(args, plan.HWInit...)
	if plan.Input != "" {
		args = append(args, "-i", plan.Input)
	}
	args = append(args, plan.Maps...)
	if len(plan.Filters) > 0 {
		args = append(args, "-vf", strings.Join(plan.Filters, ","))
	}
	args = append(args, plan.VideoArgs...)

	for i, a := range plan.Audio {
		args = append(args, fmt.Sprintf("-c:a:%d", i), a.Codec)
		if a.Action == AudioActionEncode {
			args = append(args, fmt.Sprintf("-b:a:%d", i), fmt.Sprintf("%dk", a.BitrateKbps))
		}
		if a.Language != "" {
			args = append(args, fmt.Sprintf("-metadata:s:a:%d", i), "language="+a.Language)
		}
		if a.Default {
			args = append(args, fmt.Sprintf("-disposition:a:%d", i), "default")
		}
	}

	// -c:s/-c:t are emitted unconditionally: harmless when no subtitle or
	// attachment stream is actually mapped, and this keeps the argv shape
	// deterministic per profile+tier rather than per source.
	args = append(args, "-c:s", "copy", "-c:t", "copy")

	if tag, ok := plan.Tags["CLUSTARR_PROFILE"]; ok {
		args = append(args, "-metadata", "CLUSTARR_PROFILE="+tag)
	}

	if flags := movFlags(plan.Container); flags != "" {
		args = append(args, "-movflags", flags)
	}

	args = append(args, "-f", containerFormatName(plan.Container), plan.Output)
	return args
}

// movFlags is the one -movflags value an output container gets: every mp4
// muxer flag "+"-joined into a single option, so the argv (and its golden)
// states the muxer's whole flag set in one place. Empty for a container that
// is not mp4.
//
//   - +faststart moves the moov atom to the front, so a player can start
//     before the whole file is read.
//   - +use_metadata_tags makes the mp4 muxer write arbitrary global tags.
//     Without it the muxer keeps only the iTunes-style keys it knows and
//     drops CLUSTARR_PROFILE without a word, so a rescan could never
//     recognise the file as transcoded -- the tag is how the probe
//     (pkg/mediainfo, status.mediaInfo.transcodeProfile) and squasharr's
//     already-tagged check see an earlier transcode. The cost: the muxer
//     then writes every global tag as an mdta key, the title included,
//     rather than as an iTunes atom (©nam).
func movFlags(c Container) string {
	if c != ContainerMP4 {
		return ""
	}
	return "+faststart+use_metadata_tags"
}
