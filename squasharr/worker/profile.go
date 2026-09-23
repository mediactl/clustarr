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

package worker

import (
	"k8s.io/utils/ptr"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// ProfileSpec converts a TranscodeProfile's CRD spec into pkg/transcode's
// plain-Go mirror of it, field for field. The Job-scheduling fields
// (default, selector, resources, gpu, scratch, priority, activeDeadline,
// ttlSecondsAfterFinished, chunking) have no counterpart there, by design:
// see transcode.ProfileSpec's own doc comment.
//
// It is the ONE converter in squasharr. The TranscodeProfile controller
// hashes its result into status.hash (with hardware nil), the TranscodeJob
// controller plans from it, and this worker executes it; a field dropped
// here is therefore dropped from all three at once, and
// transcodeprofile's TestStatusHashChangesWithEveryRenderField fails by
// name.
//
// hardware, when non-nil, is TranscodeJob.spec.hardware, which overrides
// the profile's encoder backend for one job.
func ProfileSpec(spec transcodev1alpha1.TranscodeProfileSpec, hardware *transcodev1alpha1.Hardware) transcode.ProfileSpec {
	hw := spec.Hardware
	if hardware != nil && *hardware != "" {
		hw = *hardware
	}
	v := spec.Video
	return transcode.ProfileSpec{
		Container: transcode.Container(spec.Container),
		Hardware:  transcode.Hardware(hw),
		Video: transcode.VideoSpec{
			Codec:       v.Codec,
			PixelFormat: v.PixelFormat,
			Profile:     v.Profile,
			CRF: transcode.CRFTable{
				SD: v.CRF.SD, HD: v.CRF.HD, UHD: v.CRF.UHD, HDROffset: v.CRF.HDROffset,
			},
			Preset:          v.Preset,
			Tune:            v.Tune,
			KeyintFactor:    v.KeyintFactor,
			BFrames:         v.BFrames,
			Refs:            v.Refs,
			RCLookahead:     v.RCLookahead,
			AQMode:          v.AQMode,
			MaxRateKbps:     v.MaxRateKbps,
			BufSizeKbps:     v.BufSizeKbps,
			ExtraX265Params: v.ExtraX265Params,
			NVENC: transcode.NVENCSpec{
				Preset: v.NVENC.Preset, Tune: v.NVENC.Tune, CQ: v.NVENC.CQ,
				Multipass: v.NVENC.Multipass, BRefMode: v.NVENC.BRefMode,
			},
			QSV: transcode.QSVSpec{
				GlobalQuality: v.QSV.GlobalQuality, Preset: v.QSV.Preset, LookAheadDepth: v.QSV.LookAheadDepth,
			},
		},
		Audio: transcode.AudioSpec{
			Codec:                 spec.Audio.Codec,
			BitratePerChannelKbps: spec.Audio.BitratePerChannelKbps,
			KeepOriginal:          transcode.KeepOriginalPolicy(spec.Audio.KeepOriginal),
			Languages:             spec.Audio.Languages,
			DropCommentary:        spec.Audio.DropCommentary,
			StereoCompatTrack:     spec.Audio.StereoCompatTrack,
		},
		Subtitles: transcode.SubSpec{
			CopyText:        spec.Subtitles.CopyText,
			CopyBitmap:      spec.Subtitles.CopyBitmap,
			CopyAttachments: spec.Subtitles.CopyAttachments,
		},
		HDR: transcode.HDRSpec{
			HDR10Plus:   transcode.HDR10PlusMode(spec.HDR.HDR10Plus),
			DolbyVision: transcode.DolbyVisionMode(spec.HDR.DolbyVision),
		},
		Policy: transcode.PolicySpec{
			SkipIfCompliant:             spec.Policy.SkipIfCompliant,
			RemuxOnlyWhenVideoCompliant: spec.Policy.RemuxOnlyWhenVideoCompliant,
			NeverTranscodeModifiers:     spec.Policy.NeverTranscodeModifiers,
			MinDuration:                 spec.Policy.MinDuration.Duration,
			MaxOutputToSourcePercent:    spec.Policy.MaxOutputToSourcePercent,
			ReplaceSource:               ReplaceSource(spec.Policy),
			RecycleBin:                  RecycleBin(spec.Policy),
		},
		Verify: transcode.VerifySpec{
			PacketCount:   spec.Verify.PacketCount,
			FullDecode:    spec.Verify.FullDecode,
			VMAFMinCentis: spec.Verify.VMAFMinCentis,
		},
	}
}

// ReplaceSource is policy.replaceSource with its CRD default applied: unset
// means true. The pointer exists so a Go client can say false; nil must
// still read as the default, because a spec built in Go and never
// round-tripped through the apiserver has nil here, and the kubebuilder
// default is applied only to what the apiserver stores.
func ReplaceSource(p transcodev1alpha1.PolicySpec) bool { return ptr.Deref(p.ReplaceSource, true) }

// RecycleBin is policy.recycleBin with its CRD default applied: unset
// means true.
func RecycleBin(p transcodev1alpha1.PolicySpec) bool { return ptr.Deref(p.RecycleBin, true) }
