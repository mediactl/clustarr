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

	"github.com/dlclark/regexp2"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/subtitleplan"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// Reasons this controller sets on SubtitleProfile.status.conditions, beyond
// k8s.Reason* and the api package's SubtitleProfileConditionReady/Invalid.
const (
	// ConditionOverlap is a condition type this controller invented, mirroring
	// app/transcode/controller/transcodeprofile's identical ruling: "pick
	// deterministically, never create two requests, and surface it (a
	// condition on the losing profile is reasonable)." Overlap=True never
	// changes which files this profile wins; it is informational for the
	// files it does not.
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

	// ReasonInvalidFilter is Invalid's reason when a mustContain or
	// mustNotContain pattern does not compile under regexp2 (loop spec
	// §2.13), which the fetch worker could only report per task.
	ReasonInvalidFilter = "InvalidFilter"
)

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

// validateProfile reports whether sp cannot be used at all, and why. It
// checks three static (data-independent) shapes:
//
//  1. Two profiles both setting spec.default. The CRD's own doc comment says
//     the controller sets Invalid "on the newer one", so the loser here is
//     whichever one subtitleplan.ProfileLess ranks behind the winner across all profiles
//     currently marked default.
//  2. A LanguageItem whose own Key does not match its canonical derivation --
//     see [ReasonKeyMismatch]'s doc comment for why this controller, not
//     pkg/subtitles.Plan, is where that gets enforced.
//  3. A mustContain or mustNotContain pattern that does not compile
//     ([ReasonInvalidFilter]).
//
// There is no per-file decision here: pkg/subtitles.Plan's inputs (existing
// subtitles, audio languages) are the remediation loop's subtitles planner's,
// computed from a specific MediaFile (loop spec §6.4).
func validateProfile(sp *subtitlev1alpha1.SubtitleProfile, all []subtitlev1alpha1.SubtitleProfile) (invalid bool, reason, message string) {
	if sp.Spec.Default {
		if winner := subtitleplan.DefaultWinner(all); winner != nil && winner.Name != sp.Name {
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
	for _, f := range []struct {
		field    string
		patterns []string
	}{{"mustContain", sp.Spec.MustContain}, {"mustNotContain", sp.Spec.MustNotContain}} {
		for _, pat := range f.patterns {
			if _, err := regexp2.Compile(pat, regexp2.IgnoreCase); err != nil {
				return true, ReasonInvalidFilter, fmt.Sprintf("%s %q does not compile: %v", f.field, pat, err)
			}
		}
	}
	return false, "", ""
}
