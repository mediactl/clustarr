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
	"time"

	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// ProfileSpec converts a TranscodeProfile's CRD spec into pkg/transcode's
// plain-Go mirror of it, field for field. The Job-scheduling and admission
// fields (default, selector, resources, gpu, scratch, priority,
// maxConcurrent, activeDeadline, ttlSecondsAfterFinished, chunking) have no
// counterpart there, by design:
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
	if hardware != nil && *hardware != "" && *hardware != transcodev1alpha1.HardwareAuto {
		hw = *hardware
	}
	if hw == transcodev1alpha1.HardwareAuto || hw == "" {
		hw = transcodev1alpha1.HardwareCPU // auto with no class chosen yet plans for CPU
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
				SD: v.CRF.SD, HD: v.CRF.HD, UHD: v.CRF.UHD, HDROffset: v.CRF.HDROffsetOrDefault(),
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
			DropCommentary:        ptr.Deref(spec.Audio.DropCommentary, true),
			StereoCompatTrack:     spec.Audio.StereoCompatTrack,
		},
		Subtitles: transcode.SubSpec{
			CopyText:        ptr.Deref(spec.Subtitles.CopyText, true),
			CopyBitmap:      ptr.Deref(spec.Subtitles.CopyBitmap, true),
			CopyAttachments: ptr.Deref(spec.Subtitles.CopyAttachments, true),
		},
		HDR: transcode.HDRSpec{
			HDR10Plus:   transcode.HDR10PlusMode(spec.HDR.HDR10Plus),
			DolbyVision: transcode.DolbyVisionMode(spec.HDR.DolbyVision),
		},
		Policy: transcode.PolicySpec{
			SkipIfCompliant:             ptr.Deref(spec.Policy.SkipIfCompliant, true),
			RemuxOnlyWhenVideoCompliant: ptr.Deref(spec.Policy.RemuxOnlyWhenVideoCompliant, true),
			NeverTranscodeModifiers:     spec.Policy.NeverTranscodeModifiers,
			MinDuration:                 MinDuration(spec.Policy),
			MaxOutputToSourcePercent:    MaxOutputToSourcePercent(spec.Policy),
			ReplaceSource:               ReplaceSource(spec.Policy),
			RecycleBin:                  RecycleBin(spec.Policy),
		},
		Verify: transcode.VerifySpec{
			PacketCount:   ptr.Deref(spec.Verify.PacketCount, true),
			FullDecode:    spec.Verify.FullDecode,
			VMAFMinCentis: spec.Verify.VMAFMinCentis,
		},
	}
}

// HasProfileTag reports whether the MediaFile records tag ("<profile>@<hash>")
// for its file. catalogarr records a tag two ways, and either counts:
//
//   - its probe reads the file's own CLUSTARR_PROFILE into
//     status.mediaInfo.transcodeProfile -- the tag this worker's live probe
//     of the same bytes reads, and the only record an earlier install's
//     output, found by a rescan, has;
//   - a transcode it incorporates sets status.transcode.profileTag: an
//     in-place swap (whose probe soon reads the same tag), or a
//     replaceSource=false job whose derived copy sits beside this untouched
//     file, which then keeps its own probe tag, if any.
//
// catalogarr drops profileTag when the bytes change without a transcode,
// so a match there is never stale. The TranscodeProfile controller (is this
// file already this profile's work?) and the TranscodeJob controller's
// planner both ask here, so the two cannot disagree.
func HasProfileTag(mf *catalogv1alpha1.MediaFile, tag string) bool {
	if tag == "" {
		return false
	}
	if mi := mf.Status.MediaInfo; mi != nil && mi.TranscodeProfile == tag {
		return true
	}
	return mf.Status.Transcode != nil && mf.Status.Transcode.ProfileTag == tag
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

// DefaultMinDuration mirrors policy.minDuration's +kubebuilder:default="1m".
const DefaultMinDuration = time.Minute

// MinDuration is policy.minDuration with its CRD default applied: unset
// means [DefaultMinDuration], an explicit 0s considers every file.
func MinDuration(p transcodev1alpha1.PolicySpec) time.Duration {
	if p.MinDuration == nil {
		return DefaultMinDuration
	}
	return p.MinDuration.Duration
}

// DefaultMaxOutputToSourcePercent mirrors policy.maxOutputToSourcePercent's
// +kubebuilder:default=100.
const DefaultMaxOutputToSourcePercent = 100

// MaxOutputToSourcePercent is policy.maxOutputToSourcePercent with its CRD
// default applied: unset means 100, an explicit 0 disables the check.
func MaxOutputToSourcePercent(p transcodev1alpha1.PolicySpec) int32 {
	return ptr.Deref(p.MaxOutputToSourcePercent, DefaultMaxOutputToSourcePercent)
}

// DefaultActiveDeadline is the per-task deadline when the profile sets none.
// squasharr/controller/pool/template_test.go:TestFlooredDefaultsMatchTheGeneratedCRD
// holds it to the CRD default.
const DefaultActiveDeadline = 48 * time.Hour

// ActiveDeadline is a profile's per-task deadline, enforced by the worker.
func ActiveDeadline(p transcodev1alpha1.TranscodeProfileSpec) time.Duration {
	if p.ActiveDeadline.Duration > 0 {
		return p.ActiveDeadline.Duration
	}
	return DefaultActiveDeadline
}
