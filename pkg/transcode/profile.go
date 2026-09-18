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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Container is the output container format written by a transcode.
type Container string

// Output containers.
const (
	ContainerMKV Container = "mkv"
	ContainerMP4 Container = "mp4"
)

// Hardware selects the encoder backend a transcode runs on.
type Hardware string

// Hardware backends.
const (
	HardwareCPU    Hardware = "cpu"
	HardwareNVIDIA Hardware = "nvidia"
	HardwareIntel  Hardware = "intel"
)

// CRFTable holds the x265 constant-rate-factor per source resolution class.
type CRFTable struct{ SD, HD, UHD, HDROffset int32 }

// NVENCSpec tunes the NVIDIA NVENC encoder (hardware=nvidia).
type NVENCSpec struct {
	Preset, Tune        string
	CQ                  int32
	Multipass, BRefMode string
}

// QSVSpec tunes the Intel Quick Sync encoder (hardware=intel).
type QSVSpec struct {
	GlobalQuality  int32
	Preset         string
	LookAheadDepth int32
}

// VideoSpec describes how the video stream is encoded.
type VideoSpec struct {
	Codec, PixelFormat, Profile                      string
	CRF                                              CRFTable
	Preset                                           string
	Tune                                             *string
	KeyintFactor, BFrames, Refs, RCLookahead, AQMode int32
	MaxRateKbps, BufSizeKbps                         *int32
	ExtraX265Params                                  map[string]string
	NVENC                                            NVENCSpec
	QSV                                              QSVSpec
}

// KeepOriginalPolicy says when the original audio track is kept alongside
// (or instead of) the re-encoded one.
type KeepOriginalPolicy string

// Keep-original policies.
const (
	KeepOriginalNever    KeepOriginalPolicy = "never"
	KeepOriginalLossless KeepOriginalPolicy = "lossless"
	KeepOriginalAtmos    KeepOriginalPolicy = "atmos"
	KeepOriginalAlways   KeepOriginalPolicy = "always"
)

// AudioSpec describes how audio tracks are handled.
type AudioSpec struct {
	Codec                 string
	BitratePerChannelKbps int32
	KeepOriginal          KeepOriginalPolicy
	Languages             []string
	DropCommentary        bool
	StereoCompatTrack     bool
}

// SubSpec describes how subtitle tracks and attachments are handled.
type SubSpec struct{ CopyText, CopyBitmap, CopyAttachments bool }

// HDR10PlusMode says what happens to HDR10+ dynamic metadata.
type HDR10PlusMode string

// HDR10+ modes.
const HDR10PlusDrop HDR10PlusMode = "drop"

// DolbyVisionMode says what happens to Dolby Vision sources.
type DolbyVisionMode string

// Dolby Vision modes.
const (
	DolbyVisionPassthrough      DolbyVisionMode = "passthrough"
	DolbyVisionDowngradeToHDR10 DolbyVisionMode = "downgradeToHDR10"
	DolbyVisionReject           DolbyVisionMode = "reject"
)

// HDRSpec describes how high-dynamic-range metadata is handled.
type HDRSpec struct {
	HDR10Plus   HDR10PlusMode
	DolbyVision DolbyVisionMode
}

// PolicySpec decides which files are transcoded and what happens afterwards.
type PolicySpec struct {
	SkipIfCompliant             bool
	RemuxOnlyWhenVideoCompliant bool
	NeverTranscodeModifiers     []string
	MinDuration                 time.Duration // mirrors metav1.Duration
	// MaxOutputToSourcePercent mirrors the CRD's scaled int (a percent, 100
	// == 1.0x); the design spec's §4.5 pseudocode instead shows a
	// MaxOutputToSourceRatio float64 = 1.0 -- the real generated
	// api/transcode/v1alpha1.PolicySpec field (and CLAUDE.md's float ban)
	// wins.
	MaxOutputToSourcePercent  int32
	ReplaceSource, RecycleBin bool
}

// VerifySpec describes post-encode verification.
type VerifySpec struct {
	PacketCount   bool   // reserved: exact packet-count comparison is deferred past this task, see Verify()
	FullDecode    bool   // reserved: not implemented by Verify() in this task, see Verify()
	VMAFMinCentis *int32 // reserved: VMAF is not implemented by Verify() in this task
}

// ProfileSpec is pkg/transcode's plain-Go mirror of TranscodeProfileSpec. It
// deliberately omits Default, Selector, Resources, GPU, Scratch, Priority,
// ActiveDeadline, TTLSecondsAfterFinished and Chunking: those are Kubernetes
// Job-scheduling fields the squasharr controller resolves before calling
// this package, never ffmpeg-render inputs. The x265 thread-pool size
// (Resources.Limits[cpu], fed via the Downward API per note §3.7) is passed
// explicitly as PlanMeta.Threads instead of smuggled through this struct.
type ProfileSpec struct {
	Container Container
	Hardware  Hardware
	Video     VideoSpec
	Audio     AudioSpec
	Subtitles SubSpec
	HDR       HDRSpec
	Policy    PolicySpec
	Verify    VerifySpec
}

// ProfileHash is the plain-Go analogue of the spec's
// transcodev1alpha1-typed ProfileHash(spec) string: sha256 of the
// render-relevant spec, hex-encoded. Deterministic: encoding/json sorts
// map[string]string keys on Marshal, so ExtraX265Params does not need
// separate sorting here.
func ProfileHash(p ProfileSpec) string {
	b, _ := json.Marshal(p) // encoding/json sorts map keys; struct field order is fixed by declaration order
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
