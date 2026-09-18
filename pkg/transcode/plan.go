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
	"path/filepath"
	"strings"
)

// Decision is Plan's top-level verdict for one source file.
type Decision string

// Decisions.
const (
	DecisionSkip      Decision = "skip"
	DecisionRemuxOnly Decision = "remuxOnly"
	DecisionEncode    Decision = "encode"
	DecisionReject    Decision = "reject"
)

// Tier is the encoder backend a Plan renders against.
type Tier string

// Tiers.
const (
	TierCPUx265 Tier = "cpu-x265"
	TierNVENC   Tier = "nvenc"
	TierQSV     Tier = "qsv"
	TierVAAPI   Tier = "vaapi"
)

// AudioAction is what happens to one source audio track.
type AudioAction string

// Audio actions.
const (
	AudioActionEncode AudioAction = "encode"
	AudioActionCopy   AudioAction = "copy"
	AudioActionDrop   AudioAction = "drop"
)

// AudioTrackPlan is a superset of the CRD's status-facing AudioPlan (adds
// Language, needed to render -metadata:s:a:N language=...; the controller
// trims it when writing TranscodeJobStatus.Plan.AudioTracks).
type AudioTrackPlan struct {
	SourceIndex int32
	Action      AudioAction
	Codec       string
	BitrateKbps int32
	Language    string
	Default     bool
}

// HDRParams is the CRD status mirror of the HDR handling Plan chose.
type HDRParams struct {
	Mode               string // "none" | "hdr10" | "hlg" | "dolbyVision"
	MasterDisplay      string // rendered x265 master-display= value, "" if absent
	MaxCLL             string // rendered x265 max-cll= value, "" if absent
	DolbyVisionProfile int32  // 0 when not DV
	RequiresVBV        bool
}

// Expectation is what Verify checks the output against.
type Expectation struct {
	DurationTolMillis int64
	Streams           int32
	VideoCodec        string
	PixelFormat       string
	MinOutputBytes    int64
}

// PlanResult is the Plan function's full rendered decision for one source
// file. (Named PlanResult, not Plan, because the function that produces it
// is named Plan -- Go does not allow a type and a function to share an
// identifier in the same package; the task brief's Produces block shows
// both named "Plan", which is why this package deviates on the type's
// name.)
type PlanResult struct {
	Decision    Decision
	Reason      string
	Tier        Tier
	Container   Container
	Input       string
	Output      string // "<stem>.part.<ext>"; caller verifies then atomically replaces the source (pkg/fsops, outside this package)
	HWInit      []string
	Maps        []string
	Filters     []string
	VideoArgs   []string
	Audio       []AudioTrackPlan
	Subtitles   []int32 // source stream indexes copied
	Attachments bool
	HDR         HDRParams
	Tags        map[string]string // {"CLUSTARR_PROFILE": "<name>@<hash>"}
	Expect      Expectation
}

// PlanMeta carries the inputs Plan needs that are not part of the CRD spec:
// the profile's ObjectMeta.Name and controller-computed ProfileHash (Plan
// has no Kubernetes client to look these up itself), and the resolved x265
// thread-pool size (note §3.7: x265 reads host CPU count, not the cgroup
// quota, so the caller must resolve limits.cpu via the Downward API and
// pass it down explicitly).
type PlanMeta struct {
	ProfileName string
	ProfileHash string
	Threads     int32
}

// containerFromExt maps a lowercased file extension (without the leading
// dot) to a Container, reporting false when it is not one we recognise.
func containerFromExt(ext string) (Container, bool) {
	switch strings.ToLower(strings.TrimPrefix(ext, ".")) {
	case "mkv":
		return ContainerMKV, true
	case "mp4":
		return ContainerMP4, true
	default:
		return "", false
	}
}

func videoCompliant(v VideoStream) bool {
	return v.Codec == "hevc" && v.Profile == "Main 10" && v.PixFmt == "yuv420p10le"
}

func audioCompliant(info MediaInfo, profile ProfileSpec) bool {
	for _, a := range info.Audio {
		if a.Codec != profile.Audio.Codec {
			return false
		}
	}
	return true
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// SelectTier picks the encoder tier from profile.Hardware and info's HDR
// state alone (no capability awareness) -- Dolby Vision forces cpu-x265
// regardless of profile.Hardware (note §3.5: "No hardware encoder... writes
// RPUs -> DV becomes HDR10 (P8) or garbage (P5). Force libx265 for DV
// sources."). Exported so a worker can pre-validate a profile before
// creating a Job.
func SelectTier(profile ProfileSpec, info MediaInfo) (Tier, error) {
	if len(info.Video) > 0 && hdrClass(info.Video[0].HDR.Format) == hdrDolbyVision {
		return TierCPUx265, nil // note §3.5: never a hardware encoder for DV
	}
	switch profile.Hardware {
	case HardwareCPU, "":
		return TierCPUx265, nil
	case HardwareNVIDIA:
		return TierNVENC, nil
	case HardwareIntel:
		return TierQSV, nil // capability-aware fallback to vaapi happens in Plan via FallbackTier
	default:
		return "", fmt.Errorf("transcode: unknown hardware %q", profile.Hardware)
	}
}

// Plan decides skip / remuxOnly / encode / reject and renders every
// ffmpeg-facing field of Plan deterministically. Pure: no I/O, no
// filesystem access, no ffmpeg/ffprobe invocation. caps is precomputed once
// per worker by ProbeCapabilities and passed in as data so Plan itself
// never blocks.
func Plan(info MediaInfo, profile ProfileSpec, caps Capabilities, meta PlanMeta) (*PlanResult, error) {
	if len(info.Video) == 0 {
		return nil, fmt.Errorf("transcode: Plan: info has no video stream")
	}
	v0 := info.Video[0]

	container, hasContainer := containerFromExt(filepath.Ext(info.Path))

	plan := &PlanResult{Container: profile.Container, Input: info.Path}

	tagged := meta.ProfileName != "" && info.Tags != nil &&
		info.Tags["CLUSTARR_PROFILE"] == meta.ProfileName+"@"+meta.ProfileHash
	compliant := videoCompliant(v0) && audioCompliant(info, profile) && hasContainer && container == profile.Container

	if compliant {
		plan.Decision = DecisionSkip
		plan.Reason = "already compliant with profile"
		return plan, nil
	}
	if tagged {
		plan.Decision = DecisionSkip
		plan.Reason = "tagged with current profile hash"
		return plan, nil
	}

	if containsString(profile.Policy.NeverTranscodeModifiers, info.Modifier) {
		plan.Decision = DecisionSkip
		plan.Reason = fmt.Sprintf("modifier %q is in policy.neverTranscodeModifiers", info.Modifier)
		return plan, nil
	}

	if info.Format.Duration < profile.Policy.MinDuration {
		plan.Decision = DecisionSkip
		plan.Reason = "source duration below policy.minDuration"
		return plan, nil
	}

	class := hdrClass(v0.HDR.Format)
	if class == hdrDolbyVision {
		switch profile.HDR.DolbyVision {
		case DolbyVisionReject:
			plan.Decision = DecisionReject
			plan.Reason = "source is Dolby Vision and hdr.dolbyVision policy is reject"
			return plan, nil
		case DolbyVisionPassthrough:
			if profile.Video.MaxRateKbps == nil || profile.Video.BufSizeKbps == nil {
				plan.Decision = DecisionReject
				plan.Reason = "hdr.dolbyVision is passthrough but video.maxRateKbps/bufSizeKbps are unset (required for DV VBV)"
				return plan, nil
			}
		}
	}

	tier, err := SelectTier(profile, info)
	if err != nil {
		return nil, err
	}
	plan.Tier = tier

	if videoCompliant(v0) {
		plan.Decision = DecisionRemuxOnly
		plan.Reason = "video already compliant; remuxing audio/subtitles/container only"
		return plan, nil
	}

	plan.Decision = DecisionEncode
	reason := "video requires transcoding to hevc/main10/yuv420p10le"
	if class == hdrDolbyVision && v0.HDR.DolbyVision != nil && v0.HDR.DolbyVision.Profile == 7 {
		reason += ", profile 7 dual-layer Dolby Vision downgraded to HDR10 (enhancement layer dropped)"
	}
	plan.Reason = reason
	return plan, nil
}

// resolutionClass buckets a source's frame height into the three CRFTable
// tiers.
func resolutionClass(height int32) string {
	switch {
	case height <= 576:
		return "sd"
	case height <= 1080:
		return "hd"
	default:
		return "uhd"
	}
}

// CRFFor picks the x265 constant-rate-factor for a source of the given
// height, applying the table's HDROffset when hdr is true. Exported so the
// Args golden fixtures can assert against it independently of Plan.
func CRFFor(t CRFTable, height int32, hdr bool) int32 {
	var base int32
	switch resolutionClass(height) {
	case "sd":
		base = t.SD
	case "hd":
		base = t.HD
	default:
		base = t.UHD
	}
	if hdr {
		base += t.HDROffset
	}
	return base
}
