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
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Capabilities reports which of the four tiers' hardware encoders are
// present in this node's ffmpeg build.
type Capabilities struct{ Encoders map[Tier]bool }

// encoderLine is the ffmpeg encoder name each tier's line in `ffmpeg
// -encoders` output carries.
var encoderLine = map[Tier]string{
	TierCPUx265: "libx265",
	TierNVENC:   "hevc_nvenc",
	TierQSV:     "hevc_qsv",
	TierVAAPI:   "hevc_vaapi",
}

// ParseCapabilities is the pure parser behind ProbeCapabilities: it reads
// `ffmpeg -hide_banner -encoders` output and reports which of our four
// tiers' encoders (libx265, hevc_nvenc, hevc_qsv, hevc_vaapi) are present in
// this ffmpeg build. Exported and I/O-free so it is golden-testable against
// a captured fixture.
func ParseCapabilities(encodersOutput string) Capabilities {
	caps := Capabilities{Encoders: make(map[Tier]bool, len(encoderLine))}
	for tier, name := range encoderLine {
		caps.Encoders[tier] = strings.Contains(encodersOutput, " "+name+" ") ||
			strings.Contains(encodersOutput, " "+name+"\t")
	}
	return caps
}

// ProbeCapabilities shells out to `<ffmpegPath> -hide_banner -encoders`
// once (a worker calls this at startup, not per Plan call) and parses the
// result with ParseCapabilities.
func ProbeCapabilities(ctx context.Context, ffmpegPath string) (Capabilities, error) {
	ctx, span := tracing.Start(ctx, "transcode.probe_capabilities")
	defer span.End()

	out, err := exec.CommandContext(ctx, ffmpegPath, "-hide_banner", "-encoders").Output()
	if err != nil {
		tracing.RecordError(span, err)
		return Capabilities{}, fmt.Errorf("transcode: probe capabilities: %w", err)
	}
	return ParseCapabilities(string(out)), nil
}

// FallbackTier tries the one documented intel fallback (qsv -> vaapi, note
// §4.2/§4.3: QSV is preferred when the libvpl runtime is present, VAAPI is
// the vendor-neutral fallback on the same node). Any other unavailable tier
// has no fallback and FallbackTier returns (want, false).
func FallbackTier(want Tier, caps Capabilities) (Tier, bool) {
	if caps.Encoders[want] {
		return want, true
	}
	if want == TierQSV && caps.Encoders[TierVAAPI] {
		return TierVAAPI, true
	}
	return want, false
}
