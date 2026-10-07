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

// Package transcode is squasharr's media vocabulary: the input model a
// plan is made from, and the types the standard (pkg/transcode/standard)
// and the in-process engine (pkg/transcode/engine) share. Nothing here runs
// ffmpeg; since the argv engine was deleted (ffgo spec, Phase 5) every
// transcode runs in-process on FFmpeg 9's libraries through ffgo.
//
// FromProbe adapts a github.com/mediactl/clustarr/pkg/mediainfo probe
// (commonv1.MediaInfo plus mediainfo.Raw) into this package's MediaInfo;
// FromSummary builds the same model from the commonv1.MediaInfo summary
// alone, which is all a MediaFile's status stores. The standard takes
// nothing from a probe that the summary lacks, so the controller's recorded
// plan (from the stored summary) hashes as the worker's (from a live probe).
//
// Tier, Limits and Decoders (NVDEC's measured decode set, with a static
// Turing list as the fallback) describe the device a plan runs on;
// EightBitTarget and TooManyStreams are the two rules the controller and
// the standard share; Progress and Report are the engine's telemetry and
// verification. A profile reaches the standard as standard.Profile
// (app/transcode/jobspec.StandardProfile); this package mirrors only its
// Container and Hardware enums.
//
// This package carries no Kubernetes types beyond commonv1.MediaInfo, and
// no float crosses into CRD status: Progress is scaled integers.
package transcode
