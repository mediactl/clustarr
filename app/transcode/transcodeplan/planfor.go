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

package transcodeplan

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/standard"
)

// ProfileTagKey is the container tag transcode writes into every output,
// "<name>@<hash>" (§4.5). catalog records it two ways: the probe reads it
// into MediaFile.status.mediaInfo.transcodeProfile, and a transcode it
// incorporates sets status.transcode.profileTag.
const ProfileTagKey = "CLUSTARR_PROFILE"

// MediaInfoOf builds pkg/transcode's MediaInfo from the probe the draft
// status st holds (ruling R3: the manager does not probe; the worker
// re-probes the live file and refuses on a ProbeHash mismatch), through
// transcode.FromSummary, which takes nothing from a probe the summary lacks
// -- so the plan recorded here hashes as the worker's.
//
// The summary carries no format tags. The one the standard reads, the
// CLUSTARR_PROFILE that makes a file "already transcoded", is set from
// catalog's records (recordedTag), so a transcoded file is skipped here as
// the worker's probe of it would be.
func MediaInfoOf(path string, mf *catalogv1alpha1.MediaFile, st *catalogv1alpha1.MediaFileStatus, tag string) (transcode.MediaInfo, error) {
	info, err := transcode.FromSummary(path, st.MediaInfo)
	if err != nil {
		return transcode.MediaInfo{}, err
	}
	info.Format.SizeBytes = mf.Spec.SizeBytes
	info.Modifier = string(mf.Spec.Quality.Modifier)
	if t := recordedTag(st, tag); t != "" {
		info.Tags = map[string]string{ProfileTagKey: t}
	}
	return info, nil
}

// recordedTag is the CLUSTARR_PROFILE catalog recorded for the file whose
// status is st: the probe's read of the file's own tag (any profile, any
// hash), else a transcode by tag's profile, under any hash, that catalog
// incorporated (status.transcode.profileTag; a replaceSource=false copy
// records it on the untouched source beside its derived copy). "" when
// there is neither.
func recordedTag(st *catalogv1alpha1.MediaFileStatus, tag string) string {
	if mi := st.MediaInfo; mi != nil && mi.TranscodeProfile != "" {
		return mi.TranscodeProfile
	}
	name, _, _ := strings.Cut(tag, "@")
	if tr := st.Transcode; tr != nil && strings.HasPrefix(tr.ProfileTag, name+"@") {
		return tr.ProfileTag
	}
	return ""
}

// ContainerChange reports whether planning source under a profile that
// writes container changes the file's container, and returns both sides.
// The comparison is case-insensitive on the extension; an empty container
// means the CRD default, mkv. Since gap-fix ruling R-11 a container change
// is transcoded -- to a new name beside the source (jobspec.OutputPath) --
// rather than skipped; this only labels it.
func ContainerChange(source string, container transcodev1alpha1.Container) (src, want string, changed bool) {
	src = strings.ToLower(strings.TrimPrefix(filepath.Ext(source), "."))
	want = strings.ToLower(string(container))
	if want == "" {
		want = string(transcodev1alpha1.ContainerMKV)
	}
	return src, want, src != want
}

// HardwareForEncoder maps a plan's encoder back to the slot class the task
// is budgeted against. "copy" (remux-only) needs no GPU, so it takes a CPU
// slot even under a GPU profile -- holding the one NVIDIA slot to copy a
// stream would be waste.
func HardwareForEncoder(encoder string) transcodev1alpha1.Hardware {
	switch encoder {
	case "hevc_nvenc":
		return transcodev1alpha1.HardwareNVIDIA
	case "hevc_qsv", "hevc_vaapi":
		return transcodev1alpha1.HardwareIntel
	default:
		return transcodev1alpha1.HardwareCPU
	}
}

// Device is what a class's pods published of their device: the tier they
// measured ("" when none, or they disagree) and the limits by tier.
type Device struct {
	Tier   transcode.Tier
	Limits map[transcode.Tier]transcode.Limits
}

// Planning is one plan of a file for a hardware class: the standard's plan,
// or the reason the file is refused (the stream cap, the one refusal left),
// and the source and output paths it was planned between.
type Planning struct {
	Plan            standard.Result
	Reject          string
	Source, OutPath string
}

// Skips reports whether p does no work: refused, or the standard skips.
func (p Planning) Skips() bool { return p.Reject != "" || p.Plan.Decision == standard.DecisionSkip }

// PlanFailure is a file no plan is possible for, and no retry fixes.
type PlanFailure struct{ Reason, Message string }

// PlanFor plans the file at source under tp, whose hash is hash
// (jobspec.ProfileHash, never status.hash), from the stored summary in st,
// for hardware (nil: the profile's), with the device dev the class's pods
// published (zero: the worker's own plan decides). The tier is the class's
// (jobspec.StandardTier) and the limits the class's pods published, as the
// worker's own plan is, so status.transcode.plan.planHash is the hash of the
// plan the worker runs.
func PlanFor(source string, tp *transcodev1alpha1.TranscodeProfile, hash string, mf *catalogv1alpha1.MediaFile,
	st *catalogv1alpha1.MediaFileStatus, hardware *transcodev1alpha1.Hardware, dev Device,
) (Planning, *PlanFailure) {
	source = filepath.Clean(source)
	out, err := jobspec.OutputPath(source, tp.Name, jobspec.OutputContainer, jobspec.ReplaceSource(tp.Spec.Policy))
	if err != nil {
		return Planning{}, &PlanFailure{ReasonInvalidSource, fmt.Sprintf("cannot place the output: %v", err)}
	}
	info, err := MediaInfoOf(source, mf, st, Tag(tp.Name, hash))
	if err != nil {
		return Planning{}, &PlanFailure{ReasonInvalidSource, fmt.Sprintf("planning failed: %v", err)}
	}
	p := Planning{Source: source, OutPath: out}
	// Past the stream cap the stored summary is not the file, so no plan
	// from it could be the worker's.
	if why := transcode.TooManyStreams(info); why != "" {
		p.Reject = why
		return p, nil
	}
	tier := jobspec.StandardTier(jobspec.ProfileHardware(tp.Spec, hardware))
	if dev.Tier != "" {
		tier = dev.Tier // what the class's pods measured: the worker plans with it too
	}
	p.Plan = standard.Plan(info, jobspec.StandardProfile(tp.Name, hash, tp.Spec),
		standard.Hardware{Tier: tier, Limits: dev.Limits[tier]})
	return p, nil
}

// StatusPlan is status.transcode.plan for a plan that encodes or remuxes;
// a skip records none (TranscodeMode has no skip, §2.5). Dropped names the
// subtitles the plan drops (D44), replacing c0fb39b7's Planned-condition
// suffix; mediafilestatus.Render caps the list at eight. Every plan, the
// window's and the dispatch's, writes the whole block, so a re-plan
// rewrites the list.
func StatusPlan(s *standard.Result) *catalogv1alpha1.TranscodePlan {
	p := &catalogv1alpha1.TranscodePlan{
		PlanHash: s.Hash(), VideoAction: s.Video.Action, Decode: s.Video.Decode, HDRMode: s.Video.HDR,
		Dropped: slices.Clone(s.Dropped),
	}
	switch s.Decision {
	case standard.DecisionSkip:
		return nil
	case standard.DecisionCopyVideo:
		p.Mode, p.Encoder = catalogv1alpha1.TranscodeModeRemuxOnly, "copy"
	default:
		p.Mode, p.Encoder = catalogv1alpha1.TranscodeModeTranscode, s.Video.Encoder
	}
	return p
}

// truncate cuts s to at most n characters, as a CRD MaxLength counts them.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// tail keeps the last n characters of s: the end of an encoder's stderr is
// where its error is.
func tail(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[len(r)-n:])
}

// firstNonEmpty is a, else b.
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
