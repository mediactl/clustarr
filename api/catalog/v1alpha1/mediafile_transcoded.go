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

package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// Transcoded reports whether mf is a transcoded file. This is the one place
// the rule lives, here beside the type so every service reads the same
// predicate without importing another's: catalog through
// app/catalog/controller/rollup.Transcoded (the Movie and Episode phases, the
// cutoff, the search worker's and the RSS matcher's decision input), and
// import's completed-download import, which refuses to let an automatic
// grab replace a transcoded file (spec §8.4).
//
// The owner's rule (CLAUDE.md, "Transcoding"): a transcoded media file is the
// final destination. Its item reads Transcoded, never CutoffUnmet; it counts
// as meeting the cutoff; the wanted sweep never selects it; the decision
// engine rejects every automatic upgrade of it; and the import never lets an
// automatic grab overwrite it -- leaving only a user's interactive grab or
// manual import.
//
// A file is transcoded when any of these holds:
//
//   - spec.original is false: catalog incorporated a transcode swap and
//     took the file over (spec §8.5). It stays false for good, so a re-mux
//     that later strips the tag does not make the file upgradeable again.
//   - status.mediaInfo.transcodeProfile is set: the file carries transcode's
//     CLUSTARR_PROFILE container tag, read by the probe. This is what
//     recognises a library file an earlier install transcoded, found by a
//     rescan, whose MediaFile starts life with spec.original true.
//   - status.mediaInfo.videoEncoder names a libavcodec HEVC or AV1 encoder
//     (commonv1.MediaInfo.TranscodedElsewhere): another tool -- Tdarr, on
//     the owner's library -- transcoded the file after release. transcode
//     is not the only thing that transcodes, and the owner's rule is about
//     the file, not who made it (2026-09-29).
//
// status.transcode.profileTag deliberately does not count. A
// replaceSource=false transcode records it on the SOURCE's MediaFile (so
// transcode does not plan the same derived copy again) while this MediaFile's
// own bytes stay the untouched original, which is still upgradeable.
//
// A nil mf is not transcoded, and spec.original unset reads as its
// +kubebuilder:default, true. Only video kinds are ever transcoded (transcode
// selects movie and episode MediaFiles), so for every other kind this is
// false in practice; it does not test the kind, because a tag on a file is
// evidence whatever kind it backs. It is a plain Go method and generates
// nothing.
func (mf *MediaFile) Transcoded() bool {
	if mf == nil {
		return false
	}
	if mf.Spec.Original != nil && !*mf.Spec.Original {
		return true
	}
	mi := mf.Status.MediaInfo
	return mi != nil && (mi.TranscodeProfile != "" || mi.TranscodedElsewhere())
}

// Final reports whether mf is a transcoded file nothing automatic replaces:
// Transcoded, unless AnnotationUpgradeable says to read it as its release.
// It is catalog's and import's verdict (the item phase, the cutoff,
// TranscodedFinal, the import's refusal); transcode reads Transcoded.
func (mf *MediaFile) Final() bool {
	return mf.Transcoded() && !mf.Upgradeable()
}

// Upgradeable reports whether AnnotationUpgradeable is "true" on mf.
func (mf *MediaFile) Upgradeable() bool {
	return mf != nil && mf.Annotations[AnnotationUpgradeable] == "true"
}

// BytesPerSecond is the overall bitrate a QualityProfile's bitrateLimits
// judge mf by: its size before its first transcode swap (spec.sourceSizeBytes)
// when that was recorded, else its size now, over its probed runtime. Zero
// when either is unknown.
func (mf *MediaFile) BytesPerSecond() int64 {
	if mf == nil || mf.Status.MediaInfo == nil || mf.Status.MediaInfo.RuntimeMillis <= 0 {
		return 0
	}
	size := mf.Spec.SourceSizeBytes
	if size <= 0 {
		size = mf.Spec.SizeBytes
	}
	if size <= 0 {
		return 0
	}
	return size * 1000 / mf.Status.MediaInfo.RuntimeMillis
}

// ProbeCurrent reports whether status.mediaInfo describes the bytes the file
// holds now: a probe was incorporated (probeHash and mediaInfo set) and no
// probe of changed bytes is pending (Probed is not False for
// MediaFileReasonProbePending). status.probeHash moves only when catalog
// incorporates a probe, so while a probe of replaced bytes is queued a check
// that compares hashes still passes; a reader that plans from the summary
// holds off on this instead (spec 2026-10-06 §6.5.3). The remediation loop's
// planners are its readers (ADR-0016, loop spec §8.3, F3.2); until they land,
// the workers' own re-probe and probe-hash checks are the backstop. A
// version-only re-probe changes no condition, so the file stays current
// through it. It is a plain Go method and generates nothing.
func (mf *MediaFile) ProbeCurrent() bool {
	if mf == nil || mf.Status.ProbeHash == "" || mf.Status.MediaInfo == nil {
		return false
	}
	for i := range mf.Status.Conditions {
		if c := &mf.Status.Conditions[i]; c.Type == MediaFileConditionProbed {
			return c.Status != metav1.ConditionFalse || c.Reason != MediaFileReasonProbePending
		}
	}
	return true
}

// Describe is what the plan does to the file, for a person: a remux copies
// the video into the MP4 layout, its audio and subtitles given the layout
// (status.transcode.plan's mode remuxOnly, whose encoder is FFmpeg's
// "copy"); an encode names its encoder. "" for no plan.
func (p *TranscodePlan) Describe() string {
	switch {
	case p == nil:
		return ""
	case p.Mode == TranscodeModeRemuxOnly:
		return "remux to MP4, video copied"
	case p.Encoder != "":
		return "encode with " + p.Encoder
	}
	return "encode"
}
