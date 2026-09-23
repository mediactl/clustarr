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

package transcodeprofile

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// Reasons this controller sets on TranscodeProfile.status.conditions, beyond
// k8s.Reason* and the api package's TranscodeProfileConditionReady/Invalid.
const (
	// ConditionOverlap is a condition type this controller invented (the CRD's
	// own doc comment on Conditions only promises Ready and Invalid, but
	// nothing constrains Type to those two -- metav1.Condition's type field is
	// a free string in this CRD's schema). It reports that this profile's
	// selector matches at least one MediaFile a higher-priority profile also
	// claims, per the Phase E plan's ruling: "pick deterministically, never
	// create two jobs, and surface it (a condition on the losing profile is
	// reasonable)." Overlap=True never blocks job creation for the files this
	// profile DOES win; it is informational for the files it does not.
	ConditionOverlap = "Overlap"

	// ReasonSelectorOverlap is Overlap's reason when set True.
	ReasonSelectorOverlap = "SelectorOverlap"

	// ReasonNoOverlap is Overlap's reason when set False.
	ReasonNoOverlap = "NoOverlap"

	// ReasonDuplicateDefault is Invalid's reason when two profiles both set
	// spec.default -- the CRD's own doc comment: "Exactly one profile may be
	// the default; the controller sets Invalid on the newer one."
	ReasonDuplicateDefault = "DuplicateDefault"
)

// eligibleKinds lists the MediaRef kinds a TranscodeProfile may select.
// TranscodeProfileSpec.Selector's own doc comment: "Only video kinds (movie,
// episode) are eligible; enforced by the controller." Audio, book and comic
// MediaFiles are never candidates, regardless of whether their labels happen
// to match a selector.
func eligibleKind(k commonv1.MediaKind) bool {
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// profileHash is status.hash: pkg/transcode.ProfileHash over the profile as
// squasharr/worker.ProfileSpec converts it -- the one converter squasharr
// has, which the TranscodeJob controller plans from and the worker
// executes. This package had its own copy until E-4; two converters is two
// chances to drop a field, and a field dropped from the hash is a field
// whose edit never re-transcodes anything, since the hash names every
// TranscodeJob and is the CLUSTARR_PROFILE tag catalogarr compares.
//
// hardware is nil: TranscodeJob.spec.hardware is a per-job override of the
// backend, not a different profile, and the tag is the profile's.
//
// The Kubernetes scheduling fields (default, selector, resources, gpu,
// scratch, priority, activeDeadline, ttlSecondsAfterFinished, chunking) do
// not reach the hash, by design: they decide where and when an encode runs,
// never what it writes, so editing a CPU limit must not re-transcode a
// library. TestStatusHashChangesWithEveryRenderField holds both halves.
func profileHash(spec transcodev1alpha1.TranscodeProfileSpec) string {
	return transcode.ProfileHash(worker.ProfileSpec(spec, nil))
}

// transcodeJobName renders the exact scheme TranscodeJob's own doc comment
// pins (api/transcode/v1alpha1/transcodejob_types.go:307): "owned by the
// MediaFile and named <mediafile>-<profileHash[:8]>". This is what makes job
// creation idempotent without a side table -- the same (MediaFile, profile
// hash) pair always renders the same name, so a re-reconcile's k8s.Apply is a
// no-op, and a profile edit (new hash) renders a different name and so
// creates a new object rather than mutating the immutable spec of the old
// one (every TranscodeJobSpec field this controller sets is CEL
// self==oldSelf).
//
// The suffix is never truncated -- it is the 8 hex characters that make the
// name unique for this hash. mediaFileName is trimmed from the right only in
// the pathological case where the literal scheme would exceed the apiserver's
// name limit; every real MediaFile name (itself capped well under that limit)
// never hits this branch.
func transcodeJobName(mediaFileName, profileHash string) string {
	suffix := profileHash
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	name := mediaFileName + "-" + suffix
	if len(name) <= k8s.MaxNameLength {
		return name
	}
	overflow := len(name) - k8s.MaxNameLength
	trim := len(mediaFileName) - overflow
	if trim < 1 {
		trim = 1
	}
	return mediaFileName[:trim] + "-" + suffix
}

// profileTag renders the same "<name>@<hash>" convention
// mediafile_controller.go's transcodeProfileTag writes onto
// MediaFile.status.transcode.profileTag once a swap is incorporated, and
// pkg/transcode.Plan itself tags the ffmpeg output with (plan.go:200-201,
// PlanResult.Tags). It is the one piece of string formatting three different
// packages in three different services must agree on byte-for-byte; changing
// it here without changing the other two silently breaks the "already
// transcoded to this hash" check everywhere.
func profileTag(profileName, profileHash string) string {
	return fmt.Sprintf("%s@%s", profileName, profileHash)
}

// alreadyTranscoded reports whether mf already carries the current profile's
// tag, as worker.RecordedProfileTag reads it -- the probe's record of the
// file's own CLUSTARR_PROFILE first (an earlier install's output, found by a
// rescan, has only that), else the one catalogarr's mediafile controller
// mirrors only after incorporating a SUCCESSFUL swap. So this is false for a
// file that has never been transcoded, one whose last transcode failed, and
// one tagged with a stale (pre-edit) profile hash. The TranscodeJob planner
// reads the same function, so a file this skips is one it would skip too.
func alreadyTranscoded(mf *catalogv1alpha1.MediaFile, tag string) bool {
	return worker.RecordedProfileTag(mf) == tag
}

// probed reports whether mf has enough of a probe to plan a transcode from:
// a non-empty ProbeHash to pin TranscodeJobSpec.SourceProbeHash to (R3: the
// worker refuses to run if the live file no longer matches it) and a
// MediaInfo for the eventual TranscodeJob controller's Plan call
// (pkg/transcode.Plan requires at least one video stream). A MediaFile that
// has not been probed yet is not an error -- catalogarr's mediafile
// controller will probe it and this controller's MediaFile watch (on
// status.probeHash changing) fires again once it has.
func probed(mf *catalogv1alpha1.MediaFile) bool {
	return mf.Status.ProbeHash != "" && mf.Status.MediaInfo != nil
}

// selectorMatches reports whether p's selector matches mf's labels. A nil
// selector matches nothing -- only spec.default's fallback applies a profile
// with no selector at all -- and a malformed selector (which the apiserver's
// own schema validation should already have rejected, since
// metav1.LabelSelector's fields are themselves well-formed) matches nothing
// rather than everything, so a typo in a selector fails safe into "no jobs
// created" rather than "every file transcoded".
func selectorMatches(p *transcodev1alpha1.TranscodeProfile, mf *catalogv1alpha1.MediaFile) bool {
	if p.Spec.Selector == nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(mf.Labels))
}

// profileLess is the deterministic tie-break this package uses everywhere two
// profiles contend for the same role (both default, or both selecting the
// same file): the earlier-created profile wins, and a tie on creation
// timestamp (down to the second; two profiles created in the same API call
// batch, or under a fake clock in a test) falls back to the lexicographically
// smaller name. It is a total order, so repeated application across a whole
// profile list always converges on the same single winner regardless of
// iteration order.
func profileLess(a, b *transcodev1alpha1.TranscodeProfile) bool {
	at, bt := a.CreationTimestamp, b.CreationTimestamp
	if !at.Equal(&bt) {
		return at.Before(&bt)
	}
	return a.Name < b.Name
}

// defaultWinner returns the single profile that wins spec.default among all,
// by profileLess, or nil if none is marked default.
func defaultWinner(all []transcodev1alpha1.TranscodeProfile) *transcodev1alpha1.TranscodeProfile {
	var winner *transcodev1alpha1.TranscodeProfile
	for i := range all {
		p := &all[i]
		if !p.Spec.Default {
			continue
		}
		if winner == nil || profileLess(p, winner) {
			winner = p
		}
	}
	return winner
}

// winningProfile resolves which profile owns mf: the profileLess-least of
// every profile whose selector matches, or -- when none does -- def (the
// resolved default winner), or nil when neither applies. This is the single
// place §the Phase E plan's "two profiles select the same file" ruling is
// implemented: selector-matching profiles are tried before the default
// fallback (a profile that opts a file in by label always outranks the
// cluster default), and among selector-matching profiles the earliest-created
// (then lowest-named) one wins, deterministically, so no file is ever handed
// to two profiles in the same reconcile pass regardless of which profile's
// Reconcile happens to run.
func winningProfile(
	mf *catalogv1alpha1.MediaFile,
	profiles []transcodev1alpha1.TranscodeProfile,
	def *transcodev1alpha1.TranscodeProfile,
) *transcodev1alpha1.TranscodeProfile {
	var best *transcodev1alpha1.TranscodeProfile
	for i := range profiles {
		p := &profiles[i]
		if !selectorMatches(p, mf) {
			continue
		}
		if best == nil || profileLess(p, best) {
			best = p
		}
	}
	if best != nil {
		return best
	}
	return def
}

// itemKey names one catalog item a MediaFile can back: its MediaRef kind,
// namespace and name.
type itemKey struct {
	kind      commonv1.MediaKind
	namespace string
	name      string
}

// managedItemLists are the catalog item kinds a TranscodeProfile can select
// a file of ([eligibleKind]), with the List kind to read each through as
// metadata only.
var managedItemLists = map[commonv1.MediaKind]string{
	commonv1.MediaKindMovie:   "MovieList",
	commonv1.MediaKindEpisode: "EpisodeList",
}

// managedFiles drops every file whose catalog item no longer exists.
//
// An import list under removeAndKeep deletes a Movie or Episode but keeps
// its files AND their MediaFile records, on purpose (importarr's
// applySyncDecision; x7b-report): the record is what stops a rescan
// re-adding the item, and the user asked to keep the file while Clustarr
// stops managing it. Transcoding such a file would be managing it anyway, so
// a file whose item is gone is no candidate. The rule is level-based: it
// needs no marker, and a list re-adding the item under the same
// deterministic name makes the file a candidate again -- the item watch in
// SetupWithManager wakes every profile when that happens.
//
// items holds the movie and episode keys that exist. A file of any other
// kind is passed through untouched for eligibleKind to judge.
func managedFiles(files []catalogv1alpha1.MediaFile, items map[itemKey]bool) []catalogv1alpha1.MediaFile {
	out := make([]catalogv1alpha1.MediaFile, 0, len(files))
	for i := range files {
		ref := files[i].Spec.MediaRef
		if _, listed := managedItemLists[ref.Kind]; listed &&
			!items[itemKey{kind: ref.Kind, namespace: files[i].Namespace, name: ref.Name}] {
			continue
		}
		out = append(out, files[i])
	}
	return out
}

// selectFiles resolves every eligible file in files against the full
// profiles list and def (profiles' resolved default winner, or nil), and
// reports, from tp's point of view:
//
//   - matching: the files tp itself wins (these are what status.matchingFiles
//     counts and what the caller creates TranscodeJobs for).
//   - overlapped: true when tp's own selector matched at least one file it
//     did NOT win -- another profile's selector matched the same file and
//     beat it under profileLess. tp still wins every file selectFiles
//     returns in matching; overlapped only ever describes files tp lost.
func selectFiles(
	tp *transcodev1alpha1.TranscodeProfile,
	profiles []transcodev1alpha1.TranscodeProfile,
	def *transcodev1alpha1.TranscodeProfile,
	files []catalogv1alpha1.MediaFile,
) (matching []*catalogv1alpha1.MediaFile, overlapped bool) {
	for i := range files {
		mf := &files[i]
		if !eligibleKind(mf.Spec.MediaRef.Kind) {
			continue
		}
		winner := winningProfile(mf, profiles, def)
		if winner == nil {
			continue
		}
		switch {
		case winner.Name == tp.Name:
			matching = append(matching, mf)
		case selectorMatches(tp, mf):
			overlapped = true
		}
	}
	return matching, overlapped
}

// validateProfile reports whether tp cannot be used to plan a transcode, and
// why. It checks exactly one static (data-independent) shape: two profiles
// both setting spec.default. The CRD's own doc comment says the controller
// sets Invalid "on the newer one", so the loser here is whichever one
// profileLess ranks behind the winner across all profiles currently marked
// default.
//
// An earlier version of this function also flagged
// hdr.dolbyVision=passthrough with video.maxRateKbps/bufSizeKbps unset --
// the exact shape pkg/transcode.Plan rejects for a Dolby Vision source
// (plan.go:234-239) -- reasoning that the condition depends only on the
// profile, not on which file is being planned. An envtest proved that
// reasoning wrong: HDRSpec.DolbyVision defaults to "passthrough"
// (transcodeprofile_types.go's own +kubebuilder:default) with no default VBV
// values, so EVERY profile that does not explicitly override HDR -- which is
// most of them -- would be marked Invalid on creation and never create a
// TranscodeJob for ANY file, Dolby Vision or not, since Invalid gates job
// creation entirely (see Reconcile). The rejection is real, but it is a
// per-FILE outcome: only a source that actually IS Dolby Vision hits it, and
// R1 already gives that outcome a home -- a Skipped TranscodeJob with a
// reason, decided by task E-2's TranscodeJob controller when it calls Plan
// against that one file's real MediaInfo. Re-deriving a file-shaped decision
// from the spec alone, before any file is even looked at, blocks every
// unrelated file in the profile for a condition most of them will never
// trigger.
//
// A profile that is not Invalid is not thereby proven plannable for every
// file -- Plan still makes per-file skip/remuxOnly/encode/reject decisions
// the TranscodeJob controller (task E-2) renders -- this only catches shapes
// that are wrong regardless of which file, or whether any file, is involved.
func validateProfile(tp *transcodev1alpha1.TranscodeProfile, all []transcodev1alpha1.TranscodeProfile) (invalid bool, reason, message string) {
	if tp.Spec.Default {
		if winner := defaultWinner(all); winner != nil && winner.Name != tp.Name {
			return true, ReasonDuplicateDefault, fmt.Sprintf(
				"profile %q is also marked spec.default and takes precedence (created earlier, or sorts first on a tie)", winner.Name)
		}
	}
	return false, "", ""
}
