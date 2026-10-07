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
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
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
func EligibleKind(k commonv1.MediaKind) bool {
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// Tag renders the "<name>@<hash>" convention the transcode planner writes
// onto MediaFile.status.transcode.profileTag once a swap is incorporated, and
// pkg/transcode/standard.Plan tags the output with (Result.Tags,
// CLUSTARR_PROFILE). It is the one piece of string formatting three different
// packages in three different services must agree on byte-for-byte; changing
// it here without changing the other two silently breaks the "already
// transcoded to this hash" check everywhere.
func Tag(profileName, profileHash string) string {
	return fmt.Sprintf("%s@%s", profileName, profileHash)
}

// AlreadyTranscoded reports whether mf is done, for profile: a transcoded
// file is final (CLAUDE.md, "Transcoding"; spec §5: re-transcoding is never
// an upgrade side effect), whatever hash it was made under. That is
// catalogv1alpha1.MediaFile.Transcoded -- spec.original false, the probe's
// CLUSTARR_PROFILE (any profile, any hash: an earlier install's output, or
// this profile's before an edit or a new standard.Version), or another
// tool's HEVC or AV1 encode -- or catalog's record of a transcode by this
// profile under any hash: a replaceSource=false job records it on the
// source, whose own bytes stay untouched, and whose derived copy exists.
func AlreadyTranscoded(mf *catalogv1alpha1.MediaFile, profile string) bool {
	if mf.Transcoded() {
		return true
	}
	return mf.Status.Transcode != nil && strings.HasPrefix(mf.Status.Transcode.ProfileTag, Tag(profile, ""))
}

// Probed reports whether mf has enough of a probe to plan a transcode from:
// a non-empty ProbeHash to pin the task's sourceProbeHash to (R3: the worker
// refuses to run if the live file no longer matches it) and a MediaInfo for
// the planner to plan from.
func Probed(mf *catalogv1alpha1.MediaFile) bool {
	return mf.Status.ProbeHash != "" && mf.Status.MediaInfo != nil
}

// SelectorMatches reports whether p's selector matches l, a file's labels as
// the loop's pass will leave them (remediation.View.Labels, loop spec §3.3's
// self-trigger rule). A nil
// selector matches nothing -- only spec.default's fallback applies a profile
// with no selector at all -- and a malformed selector (which the apiserver's
// own schema validation should already have rejected, since
// metav1.LabelSelector's fields are themselves well-formed) matches nothing
// rather than everything, so a typo in a selector fails safe into "no jobs
// created" rather than "every file transcoded".
func SelectorMatches(p *transcodev1alpha1.TranscodeProfile, l map[string]string) bool {
	if p.Spec.Selector == nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(l))
}

// ProfileLess is the deterministic tie-break this package uses everywhere two
// profiles contend for the same role (both default, or both selecting the
// same file): the earlier-created profile wins, and a tie on creation
// timestamp (down to the second; two profiles created in the same API call
// batch, or under a fake clock in a test) falls back to the lexicographically
// smaller name. It is a total order, so repeated application across a whole
// profile list always converges on the same single winner regardless of
// iteration order.
func ProfileLess(a, b *transcodev1alpha1.TranscodeProfile) bool {
	at, bt := a.CreationTimestamp, b.CreationTimestamp
	if !at.Equal(&bt) {
		return at.Before(&bt)
	}
	return a.Name < b.Name
}

// DefaultWinner returns the single profile that wins spec.default among all,
// by ProfileLess, or nil if none is marked default.
func DefaultWinner(all []transcodev1alpha1.TranscodeProfile) *transcodev1alpha1.TranscodeProfile {
	var winner *transcodev1alpha1.TranscodeProfile
	for i := range all {
		p := &all[i]
		if !p.Spec.Default {
			continue
		}
		if winner == nil || ProfileLess(p, winner) {
			winner = p
		}
	}
	return winner
}

// WinningProfile resolves which profile owns the file labelled l: the ProfileLess-least of
// every profile whose selector matches, or -- when none does -- def (the
// resolved default winner), or nil when neither applies. This is the single
// place §the Phase E plan's "two profiles select the same file" ruling is
// implemented: selector-matching profiles are tried before the default
// fallback (a profile that opts a file in by label always outranks the
// cluster default), and among selector-matching profiles the earliest-created
// (then lowest-named) one wins, deterministically, so no file is ever handed
// to two profiles in the same reconcile pass regardless of which profile's
// Reconcile happens to run.
func WinningProfile(
	l map[string]string,
	profiles []transcodev1alpha1.TranscodeProfile,
	def *transcodev1alpha1.TranscodeProfile,
) *transcodev1alpha1.TranscodeProfile {
	var best *transcodev1alpha1.TranscodeProfile
	for i := range profiles {
		p := &profiles[i]
		if !SelectorMatches(p, l) {
			continue
		}
		if best == nil || ProfileLess(p, best) {
			best = p
		}
	}
	if best != nil {
		return best
	}
	return def
}

// ItemKey names one catalog item a MediaFile can back: its MediaRef kind,
// namespace and name.
type ItemKey struct {
	Kind      commonv1.MediaKind
	Namespace string
	Name      string
}

// ManagedItemLists are the catalog item kinds a TranscodeProfile can select
// a file of ([EligibleKind]), with the List kind to read each through as
// metadata only.
var ManagedItemLists = map[commonv1.MediaKind]string{
	commonv1.MediaKindMovie:   "MovieList",
	commonv1.MediaKindEpisode: "EpisodeList",
}

// ManagedFiles drops every file whose catalog item no longer exists.
//
// An import list under removeAndKeep deletes a Movie or Episode but keeps
// its files AND their MediaFile records, on purpose (import's
// applySyncDecision; x7b-report): the record is what stops a rescan
// re-adding the item, and the user asked to keep the file while Clustarr
// stops managing it. Transcoding such a file would be managing it anyway, so
// a file whose item is gone is no candidate. The rule is level-based: it
// needs no marker, and a list re-adding the item under the same
// deterministic name makes the file a candidate again -- the item watch in
// SetupWithManager wakes every profile when that happens.
//
// items holds the movie and episode keys that exist. A file of any other
// kind is passed through untouched for EligibleKind to judge.
func ManagedFiles(files []catalogv1alpha1.MediaFile, items map[ItemKey]bool) []catalogv1alpha1.MediaFile {
	out := make([]catalogv1alpha1.MediaFile, 0, len(files))
	for i := range files {
		ref := files[i].Spec.MediaRef
		if _, listed := ManagedItemLists[ref.Kind]; listed &&
			!items[ItemKey{Kind: ref.Kind, Namespace: files[i].Namespace, Name: ref.Name}] {
			continue
		}
		out = append(out, files[i])
	}
	return out
}

// SelectFiles resolves every eligible file in files against the full
// profiles list and def (profiles' resolved default winner, or nil), and
// reports, from tp's point of view:
//
//   - matching: the files tp itself wins (these are what status.matchingFiles
//     counts and what the caller plans transcodes for).
//   - overlapped: true when tp's own selector matched at least one file it
//     did NOT win -- another profile's selector matched the same file and
//     beat it under ProfileLess. tp still wins every file SelectFiles
//     returns in matching; overlapped only ever describes files tp lost.
func SelectFiles(
	tp *transcodev1alpha1.TranscodeProfile,
	profiles []transcodev1alpha1.TranscodeProfile,
	def *transcodev1alpha1.TranscodeProfile,
	files []catalogv1alpha1.MediaFile,
) (matching []*catalogv1alpha1.MediaFile, overlapped bool) {
	for i := range files {
		mf := &files[i]
		if !EligibleKind(mf.Spec.MediaRef.Kind) {
			continue
		}
		winner := WinningProfile(mf.Labels, profiles, def)
		if winner == nil {
			continue
		}
		switch {
		case winner.Name == tp.Name:
			matching = append(matching, mf)
		case SelectorMatches(tp, mf.Labels):
			overlapped = true
		}
	}
	return matching, overlapped
}

// ValidateProfile reports whether tp cannot be used to plan a transcode, and
// why. It checks exactly one static (data-independent) shape: two profiles
// both setting spec.default. The CRD's own doc comment says the controller
// sets Invalid "on the newer one", so the loser here is whichever one
// ProfileLess ranks behind the winner across all profiles currently marked
// default.
//
// A profile that is not Invalid is not thereby proven plannable for every
// file -- the standard still decides per file (skip, copy the video, encode)
// in the transcode planner -- this only catches shapes
// that are wrong regardless of which file, or whether any file, is involved.
func ValidateProfile(tp *transcodev1alpha1.TranscodeProfile, all []transcodev1alpha1.TranscodeProfile) (invalid bool, reason, message string) {
	if tp.Spec.Default {
		if winner := DefaultWinner(all); winner != nil && winner.Name != tp.Name {
			return true, ReasonDuplicateDefault, fmt.Sprintf(
				"profile %q is also marked spec.default and takes precedence (created earlier, or sorts first on a tie)", winner.Name)
		}
	}
	return false, "", ""
}
