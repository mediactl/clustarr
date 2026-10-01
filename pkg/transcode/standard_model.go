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
	"fmt"

	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// Tier is the encoder backend a plan encodes on.
type Tier string

// Tiers.
const (
	TierCPUx265 Tier = "cpu-x265"
	TierNVENC   Tier = "nvenc"
	TierQSV     Tier = "qsv"
	TierVAAPI   Tier = "vaapi"
)

// MaxStreamsPerKind is the audio, and the subtitle, stream count at which a
// source is refused: pkg/mediainfo.MaxStreamsPerKind, the cap on the probe
// summary catalogarr stores (MediaInfo.Audio and .Subtitles, MaxItems=64).
// The TranscodeJob controller plans from that summary and the worker from a
// live probe of every stream, so past the cap the two would plan different
// streams, and a summary at the cap cannot say whether any were cut. A
// count of MaxStreamsPerKind or more reads the same in both -- the summary
// holds min(n, 64), which reaches 64 exactly when n does -- so refusing
// there is one decision, with one reason, on both sides.
const MaxStreamsPerKind = mediainfo.MaxStreamsPerKind

// TooManyStreams is the refusal for a source at or past [MaxStreamsPerKind]
// of a kind, "" otherwise. It names the cap rather than the count, which
// the summary does not know past the cap, so the controller and the worker
// give the same reason.
func TooManyStreams(info MediaInfo) string {
	for _, k := range []struct {
		kind string
		n    int
	}{{"audio", len(info.Audio)}, {"subtitle", len(info.Subtitles)}} {
		if k.n >= MaxStreamsPerKind {
			return fmt.Sprintf("source has %d or more %s streams, more than the stored probe summary can hold, "+
				"so the plan could not be the worker's", MaxStreamsPerKind, k.kind)
		}
	}
	return ""
}

// EightBitTarget reports whether v is encoded HEVC Main, 8-bit, rather than
// Main 10: SDR at 1080p or less (the owner's call, 2026-10-01). HDR10, HLG
// and Dolby Vision need 10 bits for their transfer at any size, and above
// 1080p the standard stays Main 10.
func EightBitTarget(v VideoStream) bool {
	return hdrClass(v.HDR.Format) == hdrNone && v.Height > 0 && v.Height <= 1080
}

// Progress mirrors TranscodeJobStatus.Progress's scaled-int fields exactly
// -- FPSMilli, SpeedMilli, OutTimeMillis, Percent, BitrateKbps -- so a
// progress sample copies straight into the CRD status without a float
// anywhere in between. OutputBytes has no status field; it feeds the 1 Hz
// schema.TranscodeProgress telemetry.
type Progress struct {
	Frame         int64
	FPSMilli      int32
	SpeedMilli    int32
	OutTimeMillis int64
	BitrateKbps   int32
	Percent       int32
	OutputBytes   int64
}

// Report is an output's verification (engine.Verify): whether it matched
// the plan's expectation, and every way it did not.
type Report struct {
	OK             bool
	Problems       []string
	DurationMillis int64
	Streams        int32
	SizeBytes      int64
}

// Limits are what a pool pod measured of its device. NVDEC is what its
// decoder decodes; nil is unmeasured, and the static list decides.
type Limits struct {
	NVDEC *Decoders `json:"nvdec,omitempty"`
}
