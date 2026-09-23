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
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
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
// file and refuses on a ProbeHash mismatch), through
// transcode.FromSummary -- the converter whose argv the worker's
// transcode.FromProbe matches for the same bytes. So the status.plan this
// controller records, HDR arguments included, is the argv the worker runs
// (pkg/transcode's TestFromSummaryAndFromProbeRenderTheSameArgs, and this
// package's TestStatusPlanIsTheArgvTheWorkerRenders).
//
// The summary carries no format tags, so the CLUSTARR_PROFILE tag
// catalogarr mirrored into status.transcode.profileTag after a swap stands
// in for the container tag the worker reads; it only ever decides "already
// transcoded", never an argument.
func mediaInfoFromFile(path string, mf *catalogv1alpha1.MediaFile) (transcode.MediaInfo, error) {
	info, err := transcode.FromSummary(path, mf.Status.MediaInfo)
	if err != nil {
		return transcode.MediaInfo{}, err
	}
	info.Format.SizeBytes = mf.Spec.SizeBytes
	info.Modifier = string(mf.Spec.Quality.Modifier)
	if t := mf.Status.Transcode; t != nil && t.ProfileTag != "" {
		info.Tags = map[string]string{profileTagKey: t.ProfileTag}
	}
	return info, nil
}

// threadsFor is the x265 pools= size the worker will render for a Job built
// from profile: the Downward API hands it limits.cpu with divisor 1, which
// the kubelet rounds UP to whole cores (worker.ThreadsFromEnv). The same
// floored resources buildJob stamps on the container are read, so the two
// agree. A profile that sets resources but no CPU limit gets the NODE's
// allocatable CPU from the Downward API, which this controller cannot know;
// it plans with 0 there, and that job's status.plan differs from its argv in
// pools= alone (the worker logs the mismatch).
func threadsFor(profile *transcodev1alpha1.TranscodeProfile) int32 {
	limits := resourcesFor(profile).Limits
	cpu, ok := limits[corev1.ResourceCPU]
	if !ok || cpu.Sign() <= 0 {
		return 0
	}
	return int32((cpu.MilliValue() + 999) / 1000)
}

// containerChange reports whether planning source under a profile that
// writes container changes the file's container, and returns both sides for
// the Planned condition's message. The comparison is case-insensitive on the
// extension; an empty profile container means the CRD default, mkv. Since
// gap-fix ruling R-11 a container change is transcoded -- to a new name
// beside the source (worker.OutputPath) -- rather than skipped as Phase E's
// R8 did; this only labels it.
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
		ArgsHash: transcode.ArgsHash(p),
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

func capList[T any](s []T) []T {
	if len(s) > maxPlanList {
		return s[:maxPlanList]
	}
	return s
}
