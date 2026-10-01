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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/task"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// profileTagKey is the container tag squasharr writes into every output,
// "<name>@<hash>" (§4.5). catalogarr records it two ways: the probe reads it
// into MediaFile.status.mediaInfo.transcodeProfile, and a transcode it
// incorporates sets status.transcode.profileTag.
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
// The summary carries no format tags. The one the planner reads, the
// CLUSTARR_PROFILE that decides "already transcoded with this profile", is
// set to tag when the MediaFile records it (worker.HasProfileTag: the
// probe's record of the file's own tag, or the one catalogarr set after a
// transcode it incorporated). It only ever decides that skip, never an
// argument, so any other tag is left out: to Plan it means the same as none.
func mediaInfoFromFile(path string, mf *catalogv1alpha1.MediaFile, tag string) (transcode.MediaInfo, error) {
	info, err := transcode.FromSummary(path, mf.Status.MediaInfo)
	if err != nil {
		return transcode.MediaInfo{}, err
	}
	info.Format.SizeBytes = mf.Spec.SizeBytes
	info.Modifier = string(mf.Spec.Quality.Modifier)
	if worker.HasProfileTag(mf, tag) {
		info.Tags = map[string]string{profileTagKey: tag}
	}
	return info, nil
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

// statusPlanOf records p's plan as status.plan; a refused job records none.
func statusPlanOf(p planning) *transcodev1alpha1.Plan {
	if p.reject != "" {
		return nil
	}
	return standardStatusPlan(&p.plan)
}

// standardStatusPlan is status.plan for the in-process engine: its hash,
// the video's action, encoder and decode, and each track's action.
func standardStatusPlan(s *standard.Result) *transcodev1alpha1.Plan {
	out := &transcodev1alpha1.Plan{Engine: task.EngineFFgo, PlanHash: s.Hash()}
	switch s.Decision {
	case standard.DecisionSkip:
		out.Mode, out.SkipReason = transcodev1alpha1.PlanModeSkip, s.Reason
		return out
	case standard.DecisionCopyVideo:
		out.Mode, out.Encoder = transcodev1alpha1.PlanModeRemuxOnly, "copy"
	default:
		out.Mode, out.Encoder = transcodev1alpha1.PlanModeTranscode, s.Video.Encoder
	}
	out.VideoAction, out.Decode, out.HDRMode = s.Video.Action, s.Video.Decode, s.Video.HDR
	for _, a := range s.Audio {
		ap := transcodev1alpha1.AudioPlan{SourceIndex: a.SourceIndex, Action: transcodev1alpha1.AudioActionCopy}
		if a.Action == "aac" {
			ap.Action, ap.Codec, ap.BitrateKbps = transcodev1alpha1.AudioActionEncode, "aac", int32(a.BitRate/1000)
		}
		out.AudioTracks = append(out.AudioTracks, ap)
	}
	out.AudioTracks = capList(out.AudioTracks)
	out.SubtitleTracks = capList(s.Subtitles)
	return out
}

func capList[T any](s []T) []T {
	if len(s) > maxPlanList {
		return s[:maxPlanList]
	}
	return s
}
