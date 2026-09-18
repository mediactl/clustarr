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

// Package transcode decides whether and how to transcode one media file to
// squasharr's opinionated target (HEVC 10-bit, AAC, TRaSH-style muxing) and
// runs that decision through ffmpeg.
//
// Plan turns a MediaInfo probe, a ProfileSpec (this package's plain-Go
// mirror of api/transcode/v1alpha1.TranscodeProfileSpec) and a
// Capabilities snapshot into a skip/remuxOnly/encode/reject PlanResult,
// rendering every ffmpeg-facing field (HWInit, Maps, Filters, VideoArgs,
// Audio, Subtitles, HDR params, Tags) deterministically and without any
// I/O. Args then renders that PlanResult into the exact ffmpeg argv.
// Runner shells out to ffmpeg, parses its `-progress pipe:1` output into
// scaled-int Progress values (no float ever crosses into CRD status) and
// handles cancellation by sending SIGINT before escalating to SIGKILL.
// Verifier re-probes the source and output independently after an encode
// and checks duration, stream count, codec, pixel format and output size.
//
// FromProbe adapts a github.com/mediactl/clustarr/pkg/mediainfo probe
// (commonv1.MediaInfo plus mediainfo.Raw) into this package's own MediaInfo
// input model, which carries the per-stream, per-field detail an ffmpeg
// command line needs and that the CRD-facing MediaInfo has no room for.
//
// This package carries no Kubernetes types: ProfileSpec, Progress and the
// rest are plain Go, hand-mirrored from the real generated
// api/transcode/v1alpha1 types field-for-field; Phase C/E wiring converts
// between them. Whole-file jobs only -- chunked/segment-parallel encoding
// (note §9.2, ChunkSpec.Enabled must stay false in v1alpha1), HDR10+
// dynamic-metadata preservation, VMAF, full-decode verification and exact
// packet-count verification are all out of scope; see the CRD's VerifySpec
// and HDRSpec fields for what is reserved but unimplemented here.
//
// See docs/research/transcode.md for the verified ffmpeg/x265/NVENC/QSV/
// VAAPI command shapes this package's argv builders and golden tests are
// derived from, and docs/superpowers/specs/2026-09-18-clustarr-design.md
// §4.5 and §7 for the CRD field list and the pkg/transcode vocabulary.
package transcode
