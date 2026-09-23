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

package transcodejob

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// profileTagKey is the container tag catalogarr mirrors into
// MediaFile.status.transcode.profileTag after a swap, "<name>@<hash>" (§4.5).
const profileTagKey = "CLUSTARR_PROFILE"

// maxPlanList is the CRD's MaxItems on every list inside status.plan.
const maxPlanList = 200

// mediaInfoFromFile builds pkg/transcode's MediaInfo from the probe
// catalogarr already stored on the MediaFile (ruling R3: the controller role
// does not mount /data, so it cannot probe; the worker re-probes the live
// file and refuses on a ProbeHash mismatch).
//
// The stored probe is a summary, so the result is coarser than
// transcode.FromProbe's: no per-stream colour tags, no mastering-display or
// content-light side data, no chapter times. None of that changes the
// DECISION (skip / remuxOnly / encode / reject) or the tier -- those depend
// only on codec, profile, pixel format, container, audio codecs, modifier,
// duration, the CLUSTARR_PROFILE tag and the Dolby Vision profile, all of
// which are stored -- but it can change the rendered x265 HDR params. The
// status.plan this controller records is therefore the decision and a
// best-effort render; the worker renders the argv it actually runs from its
// own live probe.
//
// Stream indexes are type-relative (0:a:N), as pkg/transcode expects, not
// the container-wide indexes the stored probe carries.
func mediaInfoFromFile(path string, mf *catalogv1alpha1.MediaFile) transcode.MediaInfo {
	mi := mf.Status.MediaInfo
	info := transcode.MediaInfo{
		Path: path,
		Format: transcode.FormatInfo{
			Name:        mi.Container,
			Duration:    time.Duration(mi.RuntimeMillis) * time.Millisecond,
			SizeBytes:   mf.Spec.SizeBytes,
			BitRateKbps: int64(mi.VideoBitrateKbps),
		},
		Modifier: string(mf.Spec.Quality.Modifier),
	}
	if t := mf.Status.Transcode; t != nil && t.ProfileTag != "" {
		info.Tags = map[string]string{profileTagKey: t.ProfileTag}
	}

	v := transcode.VideoStream{
		Codec:     mi.VideoCodec,
		Profile:   mi.VideoProfile,
		PixFmt:    mi.PixelFormat,
		BitDepth:  mi.VideoBitDepth,
		Width:     mi.Width,
		Height:    mi.Height,
		FrameRate: transcode.Rational{Num: int64(mi.FpsMilli), Den: 1000},
		Duration:  info.Format.Duration,
		HDR:       transcode.HDRInfo{Format: mi.Hdr},
	}
	if mi.DoviProfile != nil {
		dv := &mediainfo.DoviRecord{Profile: *mi.DoviProfile, RPUPresent: true, BLPresent: true}
		if mi.DoviBLCompatID != nil {
			dv.BLSignalCompatibilityID = *mi.DoviBLCompatID
		}
		v.HDR.DolbyVision = dv
	}
	info.Video = []transcode.VideoStream{v}

	for i, a := range mi.Audio {
		info.Audio = append(info.Audio, transcode.AudioStream{
			Index:         int32(i),
			Codec:         a.Codec,
			Profile:       a.Profile,
			Channels:      a.Channels,
			ChannelLayout: a.ChannelLayout,
			BitRateKbps:   a.BitrateKbps,
			Lossless:      losslessAudio(a),
			Atmos:         atmosAudio(a),
			Language:      a.Language,
			Title:         a.Title,
			Disposition:   transcode.Disposition{Default: a.Default, Comment: a.Commentary},
		})
	}
	for i, s := range mi.Subtitles {
		info.Subtitles = append(info.Subtitles, transcode.SubtitleStream{
			Index:       int32(i),
			Codec:       s.Codec,
			Bitmap:      s.Bitmap,
			Language:    s.Language,
			Title:       s.Title,
			Disposition: transcode.Disposition{Forced: s.Forced, HearingImpaired: s.HearingImpaired},
		})
	}
	for i := int32(0); i < mi.Attachments; i++ {
		info.Attachments = append(info.Attachments, transcode.AttachmentStream{Index: i})
	}
	return info
}

// losslessAudio restates pkg/transcode's unexported isLosslessAudio over the
// stored stream summary; KeepOriginal=lossless depends on it.
func losslessAudio(a commonv1.AudioStream) bool {
	switch a.Codec {
	case "flac", "alac", "truehd", "mlp":
		return true
	}
	return strings.HasPrefix(a.Codec, "pcm_") || (a.Codec == "dts" && a.Profile == "DTS-HD MA")
}

// atmosAudio restates pkg/transcode's unexported isAtmos.
func atmosAudio(a commonv1.AudioStream) bool {
	return strings.Contains(strings.ToLower(a.Profile), "atmos") ||
		strings.Contains(strings.ToLower(a.Title), "atmos")
}

// containerChange reports whether planning source under a profile that
// writes container would change the file's container (ruling R8), and
// returns both sides for the message. The comparison is case-insensitive
// on the extension; an empty profile container means the CRD default, mkv.
func containerChange(source string, container transcodev1alpha1.Container) (src, want string, changed bool) {
	src = strings.ToLower(strings.TrimPrefix(filepath.Ext(source), "."))
	want = strings.ToLower(string(container))
	if want == "" {
		want = string(transcodev1alpha1.ContainerMKV)
	}
	return src, want, src != want
}

// allEncoders is the capability set the controller plans against. The
// controller cannot run `ffmpeg -encoders` on a GPU node it is not on, and
// the budget for a tier -- including a zero one -- is admission's business,
// not the planner's: a job for a tier with no slots waits in Queued rather
// than being rejected, so a GPU node that is merely down does not turn a
// library's worth of jobs Skipped. The worker plans again against its real
// capabilities (ProbeCapabilities) and can still reject there.
func allEncoders() transcode.Capabilities {
	return transcode.Capabilities{Encoders: map[transcode.Tier]bool{
		transcode.TierCPUx265: true,
		transcode.TierNVENC:   true,
		transcode.TierQSV:     true,
		transcode.TierVAAPI:   true,
	}}
}

// encoderName is the ffmpeg encoder a tier renders against, recorded as
// status.plan.encoder. A remux-only plan copies video and records "copy".
func encoderName(p *transcode.PlanResult) string {
	if p.Decision == transcode.DecisionRemuxOnly {
		return "copy"
	}
	switch p.Tier {
	case transcode.TierNVENC:
		return "hevc_nvenc"
	case transcode.TierQSV:
		return "hevc_qsv"
	case transcode.TierVAAPI:
		return "hevc_vaapi"
	default:
		return "libx265"
	}
}

// hardwareForEncoder maps status.plan.encoder back to the slot class the Job
// is budgeted against. "copy" (remux-only) needs no GPU, so it takes a CPU
// slot even under a GPU profile -- holding the one NVIDIA slot to copy a
// stream would be waste.
func hardwareForEncoder(encoder string) transcodev1alpha1.Hardware {
	switch encoder {
	case "hevc_nvenc":
		return transcodev1alpha1.HardwareNVIDIA
	case "hevc_qsv", "hevc_vaapi":
		return transcodev1alpha1.HardwareIntel
	default:
		return transcodev1alpha1.HardwareCPU
	}
}

// statusPlan renders a skip, remuxOnly or encode PlanResult as the CRD's
// status.plan. A reject decision is never rendered: per ruling R1 it lands as
// phase Skipped with status.plan unset, because PlanMode has no reject value.
func statusPlan(p *transcode.PlanResult) *transcodev1alpha1.Plan {
	if p.Decision == transcode.DecisionSkip {
		return &transcodev1alpha1.Plan{Mode: transcodev1alpha1.PlanModeSkip, SkipReason: p.Reason}
	}
	out := &transcodev1alpha1.Plan{
		Encoder:  encoderName(p),
		Mode:     transcodev1alpha1.PlanModeTranscode,
		HDRMode:  p.HDR.Mode,
		ArgsHash: argsHash(p),
	}
	if p.Decision == transcode.DecisionRemuxOnly {
		out.Mode = transcodev1alpha1.PlanModeRemuxOnly
	}
	out.VideoArgs = capList(p.VideoArgs)
	for _, a := range p.Audio {
		out.AudioTracks = append(out.AudioTracks, transcodev1alpha1.AudioPlan{
			SourceIndex: a.SourceIndex,
			Action:      transcodev1alpha1.AudioAction(a.Action),
			Codec:       a.Codec,
			BitrateKbps: a.BitrateKbps,
			Default:     a.Default,
		})
	}
	out.AudioTracks = capList(out.AudioTracks)
	out.SubtitleTracks = capList(p.Subtitles)
	return out
}

// argsHash is the sha256 of the full rendered argv, so two plans that would
// run the same ffmpeg command compare equal.
func argsHash(p *transcode.PlanResult) string {
	sum := sha256.Sum256([]byte(strings.Join(transcode.Args(p), "\x00")))
	return hex.EncodeToString(sum[:])
}

func capList[T any](s []T) []T {
	if len(s) > maxPlanList {
		return s[:maxPlanList]
	}
	return s
}
