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

package subtitleplan

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

// EligibleKind reports whether k is a video kind a SubtitleProfile may
// select. Only movie and episode MediaFiles carry a video stream subtitles
// can be searched or extracted for. Audio, book, audiobook and comic
// MediaFiles are never candidates, regardless of whether their labels
// happen to match a selector.
func EligibleKind(k commonv1.MediaKind) bool {
	return k == commonv1.MediaKindMovie || k == commonv1.MediaKindEpisode
}

// SelectorMatches reports whether p's selector matches labels. A nil
// selector matches nothing -- only spec.default's fallback applies a profile
// with no selector at all -- and a malformed selector matches nothing rather
// than everything, so a typo in a selector fails safe into "no file claimed"
// rather than "every file claimed".
func SelectorMatches(p *subtitlev1alpha1.SubtitleProfile, lbls map[string]string) bool {
	if p.Spec.Selector == nil {
		return false
	}
	sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
	if err != nil {
		return false
	}
	return sel.Matches(labels.Set(lbls))
}

// ProfileLess is the deterministic tie-break used everywhere two profiles
// contend for the same role (both default, or both selecting the same
// file): the earlier-created profile wins, and a tie on creation timestamp
// falls back to the lexicographically smaller name. It is a total order,
// so repeated application across a whole profile list always converges on
// the same single winner regardless of iteration order.
func ProfileLess(a, b *subtitlev1alpha1.SubtitleProfile) bool {
	at, bt := a.CreationTimestamp, b.CreationTimestamp
	if !at.Equal(&bt) {
		return at.Before(&bt)
	}
	return a.Name < b.Name
}

// DefaultWinner returns the single profile that wins spec.default among all,
// by ProfileLess, or nil if none is marked default.
func DefaultWinner(all []subtitlev1alpha1.SubtitleProfile) *subtitlev1alpha1.SubtitleProfile {
	var winner *subtitlev1alpha1.SubtitleProfile
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

// WinningProfile resolves which profile owns a file with labels (loop spec
// §2.13, §6.4.2): the ProfileLess-least of every profile whose selector
// matches, or -- when none does -- the default winner, or nil when neither
// applies. A profile that opts a file in by label always outranks the
// cluster default. An Invalid winner is not skipped: the file is Blocked,
// ProfileInvalid.
func WinningProfile(lbls map[string]string, profiles []subtitlev1alpha1.SubtitleProfile) *subtitlev1alpha1.SubtitleProfile {
	var best *subtitlev1alpha1.SubtitleProfile
	for i := range profiles {
		p := &profiles[i]
		if !SelectorMatches(p, lbls) {
			continue
		}
		if best == nil || ProfileLess(p, best) {
			best = p
		}
	}
	if best != nil {
		return best
	}
	return DefaultWinner(profiles)
}

// Invalid reports whether the SubtitleProfile reconciler has marked p
// unusable (a second default, a key that is not canonical, a filter that
// does not compile).
func Invalid(p *subtitlev1alpha1.SubtitleProfile) bool {
	return meta.IsStatusConditionTrue(p.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid)
}
