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

package subtitleprofile

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// Reasons this controller sets on SubtitleProfile.status.conditions, beyond
// k8s.Reason* and the api package's SubtitleProfileConditionReady/Invalid.
const (
	// ConditionOverlap is a condition type this controller invented, mirroring
	// squasharr/controller/transcodeprofile's identical ruling: "pick
	// deterministically, never create two requests, and surface it (a
	// condition on the losing profile is reasonable)." Overlap=True never
	// blocks a SubtitleRequest being ensured for the files this profile DOES
	// win; it is informational for the files it does not.
	ConditionOverlap = "Overlap"

	// ReasonSelectorOverlap is Overlap's reason when set True.
	ReasonSelectorOverlap = "SelectorOverlap"

	// ReasonNoOverlap is Overlap's reason when set False.
	ReasonNoOverlap = "NoOverlap"

	// ReasonDuplicateDefault is Invalid's reason when two profiles both set
	// spec.default -- the CRD's own doc comment: "Exactly one profile may be
	// the default; the controller sets Invalid on the newer one."
	ReasonDuplicateDefault = "DuplicateDefault"

	// ReasonKeyMismatch is Invalid's reason when a LanguageItem's own Key does
	// not match the canonical key derivable from its language, forced and hi
	// fields -- LanguageItem's own doc comment: "The canonical key is
	// derivable from language, forced and hi; the controller rejects a key
	// that does not match them." pkg/subtitles/planner.go's ProfileLanguage
	// doc comment names this controller by name as the thing that enforces
	// it: "Key must already be derivable from Language, Forced and HI exactly
	// as the SubtitleProfile controller validates it ... Plan trusts it
	// rather than re-deriving it."
	ReasonKeyMismatch = "KeyMismatch"
)

// eligibleKind reports whether k is a video kind a SubtitleProfile may
// select. Mirrors transcodeprofile's identical eligibleKind: only movie and
// episode MediaFiles carry a video stream subtitles can be searched or
// extracted for. Audio, book, audiobook and comic MediaFiles are never
// candidates, regardless of whether their labels happen to match a selector.
func eligibleKind(k commonv1.MediaKind) bool {
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// itemKey names one catalog item a MediaFile can reference.
type itemKey struct {
	kind            commonv1.MediaKind
	namespace, name string
}

// itemSet is the catalog items that currently exist, of the kinds
// [eligibleKind] admits: the Movies and Episodes one reconcile lists.
type itemSet map[itemKey]bool

// newItemSet indexes movies and episodes.
func newItemSet(movies []catalogv1alpha1.Movie, episodes []catalogv1alpha1.Episode) itemSet {
	s := make(itemSet, len(movies)+len(episodes))
	for i := range movies {
		s[itemKey{commonv1.MediaKindMovie, movies[i].Namespace, movies[i].Name}] = true
	}
	for i := range episodes {
		s[itemKey{commonv1.MediaKindEpisode, episodes[i].Namespace, episodes[i].Name}] = true
	}
	return s
}

// manages reports whether mf's item still exists: the MediaFile is one
// Clustarr manages, not a record an import list's removeAndKeep left behind.
//
// removeAndKeep (gap-fix X7b's ruling) deletes the Movie or Episode and keeps
// both the file and its MediaFile record -- the record is what stops a
// library rescan from adopting the file again and re-creating the item, and
// what a list that re-adds the item re-attaches to. Radarr deletes the file
// records outright, so from then on nothing in it touches the file; here the
// record stays, and it must not be mistaken for a managed file. A subtitle
// profile therefore skips it: the user kept the file, not Clustarr's
// management of it. The rule is level-based -- re-adding the item makes the
// file eligible again, with no marker to maintain.
func (s itemSet) manages(mf *catalogv1alpha1.MediaFile) bool {
	return s[itemKey{mf.Spec.MediaRef.Kind, mf.Namespace, mf.Spec.MediaRef.Name}]
}

// canonicalKey renders the langKey LanguageItem.Key is required to equal,
// per that field's own doc comment and pkg/subtitles/planner.go's
// ProfileLanguage doc comment (quoted on ReasonKeyMismatch). Forced wins
// over hi when both would apply, matching subtitles.FormatLangKey's own
// documented precedence (mirrored on pkg/subtitles/sidecarname.go).
func canonicalKey(l subtitlev1alpha1.LanguageItem) subtitles.LangKey {
	return subtitles.FormatLangKey(l.Language, l.Forced, l.HI == subtitlev1alpha1.HIPolicyRequired)
}

// wantedKeys renders SubtitleProfileStatus.WantedKeys: every language item's
// own key, in spec order. spec.Languages is +listType=map keyed by key, so
// the apiserver already guarantees uniqueness -- this never needs to dedupe.
func wantedKeys(spec subtitlev1alpha1.SubtitleProfileSpec) []string {
	if len(spec.Languages) == 0 {
		return nil
	}
	out := make([]string, len(spec.Languages))
	for i, l := range spec.Languages {
		out[i] = l.Key
	}
	return out
}

// selectorMatches reports whether p's selector matches mf's labels. A nil
// selector matches nothing -- only spec.default's fallback applies a profile
// with no selector at all -- and a malformed selector (which the apiserver's
// own schema validation should already have rejected, since
// metav1.LabelSelector's fields are themselves well-formed) matches nothing
// rather than everything, so a typo in a selector fails safe into "no
// requests ensured" rather than "every file claimed".
func selectorMatches(p *subtitlev1alpha1.SubtitleProfile, mf *catalogv1alpha1.MediaFile) bool {
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
// timestamp falls back to the lexicographically smaller name. It is a total
// order, so repeated application across a whole profile list always
// converges on the same single winner regardless of iteration order. Mirrors
// transcodeprofile's identical profileLess byte for byte.
func profileLess(a, b *subtitlev1alpha1.SubtitleProfile) bool {
	at, bt := a.CreationTimestamp, b.CreationTimestamp
	if !at.Equal(&bt) {
		return at.Before(&bt)
	}
	return a.Name < b.Name
}

// defaultWinner returns the single profile that wins spec.default among all,
// by profileLess, or nil if none is marked default.
func defaultWinner(all []subtitlev1alpha1.SubtitleProfile) *subtitlev1alpha1.SubtitleProfile {
	var winner *subtitlev1alpha1.SubtitleProfile
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
// resolved default winner), or nil when neither applies. Selector-matching
// profiles are tried before the default fallback (a profile that opts a file
// in by label always outranks the cluster default), and among
// selector-matching profiles the earliest-created (then lowest-named) one
// wins, deterministically, so no file is ever handed to two profiles in the
// same reconcile pass regardless of which profile's Reconcile happens to run.
func winningProfile(
	mf *catalogv1alpha1.MediaFile,
	profiles []subtitlev1alpha1.SubtitleProfile,
	def *subtitlev1alpha1.SubtitleProfile,
) *subtitlev1alpha1.SubtitleProfile {
	var best *subtitlev1alpha1.SubtitleProfile
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

// selectFiles resolves every eligible file in files -- a video kind whose
// catalog item still exists in items ([itemSet.manages]) -- against the full
// profiles list and def (profiles' resolved default winner, or nil), and
// reports, from sp's point of view:
//
//   - matching: the files sp itself wins (these are what status.matchingFiles
//     counts and what the caller ensures a SubtitleRequest for).
//   - overlapped: true when sp's own selector matched at least one file it
//     did NOT win -- another profile's selector matched the same file and
//     beat it under profileLess. sp still wins every file selectFiles
//     returns in matching; overlapped only ever describes files sp lost.
func selectFiles(
	sp *subtitlev1alpha1.SubtitleProfile,
	profiles []subtitlev1alpha1.SubtitleProfile,
	def *subtitlev1alpha1.SubtitleProfile,
	files []catalogv1alpha1.MediaFile,
	items itemSet,
) (matching []*catalogv1alpha1.MediaFile, overlapped bool) {
	for i := range files {
		mf := &files[i]
		if !eligibleKind(mf.Spec.MediaRef.Kind) || !items.manages(mf) {
			continue
		}
		winner := winningProfile(mf, profiles, def)
		if winner == nil {
			continue
		}
		switch {
		case winner.Name == sp.Name:
			matching = append(matching, mf)
		case selectorMatches(sp, mf):
			overlapped = true
		}
	}
	return matching, overlapped
}

// validateProfile reports whether sp cannot be used at all, and why. It
// checks two static (data-independent) shapes:
//
//  1. Two profiles both setting spec.default. The CRD's own doc comment says
//     the controller sets Invalid "on the newer one", so the loser here is
//     whichever one profileLess ranks behind the winner across all profiles
//     currently marked default.
//  2. A LanguageItem whose own Key does not match its canonical derivation --
//     see [ReasonKeyMismatch]'s doc comment for why this controller, not
//     pkg/subtitles.Plan, is where that gets enforced.
//
// Unlike transcodeprofile's validateProfile, there is no per-FILE decision
// to defer here: pkg/subtitles.Plan's inputs (existing subtitles, audio
// languages) are computed by the SubtitleRequest controller (task F-4) from
// a specific MediaFile, not by this controller from the profile alone, so
// there is nothing analogous to transcodeprofile's rejected
// Dolby-Vision-shaped check to avoid re-deriving here.
func validateProfile(sp *subtitlev1alpha1.SubtitleProfile, all []subtitlev1alpha1.SubtitleProfile) (invalid bool, reason, message string) {
	if sp.Spec.Default {
		if winner := defaultWinner(all); winner != nil && winner.Name != sp.Name {
			return true, ReasonDuplicateDefault, fmt.Sprintf(
				"profile %q is also marked spec.default and takes precedence (created earlier, or sorts first on a tie)", winner.Name)
		}
	}
	for _, l := range sp.Spec.Languages {
		want := canonicalKey(l)
		if string(want) != l.Key {
			return true, ReasonKeyMismatch, fmt.Sprintf(
				"language %q: key %q does not match the canonical key %q derived from language=%q forced=%t hi=%q",
				l.Language, l.Key, want, l.Language, l.Forced, l.HI)
		}
	}
	return false, "", ""
}
