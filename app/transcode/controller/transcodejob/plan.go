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
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// maxPlanList is the CRD's MaxItems on every list inside status.plan.
const maxPlanList = 200

// containerChange is transcodeplan.ContainerChange.
func containerChange(source string, container transcodev1alpha1.Container) (src, want string, changed bool) {
	return transcodeplan.ContainerChange(source, container)
}

// hardwareForEncoder is transcodeplan.HardwareForEncoder.
func hardwareForEncoder(encoder string) transcodev1alpha1.Hardware {
	return transcodeplan.HardwareForEncoder(encoder)
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
		if a.Action != standard.AudioCopy {
			ap.Action, ap.Codec, ap.BitrateKbps = transcodev1alpha1.AudioActionEncode, a.Action, int32(a.BitRate/1000)
		}
		out.AudioTracks = append(out.AudioTracks, ap)
	}
	out.AudioTracks = capList(out.AudioTracks)
	subs := make([]int32, 0, len(s.Subtitles))
	for _, sp := range s.Subtitles {
		subs = append(subs, sp.SourceIndex)
	}
	out.SubtitleTracks = capList(subs)
	return out
}

func capList[T any](s []T) []T {
	if len(s) > maxPlanList {
		return s[:maxPlanList]
	}
	return s
}
