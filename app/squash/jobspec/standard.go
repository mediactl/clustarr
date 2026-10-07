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

package jobspec

import (
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// StandardProfile is a TranscodeProfile as the standard reads it (spec §5:
// quality, audio languages, the modifier policy and the container); the
// controller and the worker both build it here, so their plans agree.
func StandardProfile(name, hash string, spec transcodev1alpha1.TranscodeProfileSpec) standard.Profile {
	return standard.Profile{
		Name: name, Hash: hash, Quality: spec.QualityOrDefault(),
		Languages: spec.Audio.Languages, NeverTranscodeModifiers: spec.Policy.NeverTranscodeModifiers,
		Container:   transcode.ContainerMP4, // the standard writes MP4 (OutputContainer)
		MinDuration: MinDuration(spec.Policy),
	}
}

// StandardTier is the tier the standard encodes on for a class (from
// ProfileHardware): the class's own encoder, Dolby Vision included -- the
// standard encodes its base layer like any HDR10 or HLG source. The
// controller and the worker both pick it here, so their plans hash alike.
func StandardTier(hw transcode.Hardware) transcode.Tier {
	switch hw {
	case transcode.HardwareNVIDIA:
		return transcode.TierNVENC
	case transcode.HardwareIntel:
		return transcode.TierQSV
	}
	return transcode.TierCPUx265
}

// CPULimitEnv carries x265's pools= size (§6.4): x265 otherwise sizes its
// pool from the host's CPU count, not the cgroup quota (note §3.7). The
// TranscodeJob controller wires it from the Downward API's limits.cpu when
// the Job's container has a CPU limit, and otherwise writes the stated
// default it planned with (its threadsFromResources) as a literal, because
// the Downward API would then report the node's CPUs.
const CPULimitEnv = "CLUSTARR_CPU_LIMIT"
