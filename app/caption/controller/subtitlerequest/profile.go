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

package subtitlerequest

import (
	"fmt"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// profileInvalid reports whether the SubtitleProfile controller has marked p
// unusable (a second default, a cutoff that names no language, ...).
func profileInvalid(p *subtitlev1alpha1.SubtitleProfile) bool {
	return k8s.IsConditionTrue(p.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid)
}

// selectProfile resolves the profile for a request with no spec.profileRef,
// per SubtitleRequestSpec.ProfileRef's doc: "select by label, falling back to
// the default profile". Among several non-default profiles whose selector
// matches the MediaFile's labels the first by name wins, so the choice is
// stable across reconciles; among several defaults (which the profile
// controller marks Invalid on all but one) the oldest valid one wins.
// Invalid profiles are never selected.
//
// It returns nil, nil when nothing applies, and an error only for a selector
// that does not parse -- which is the profile's bug, but one this request
// cannot see past, so the caller reports it rather than skipping the profile
// and silently falling through to the default.
func selectProfile(all []subtitlev1alpha1.SubtitleProfile, mfLabels map[string]string) (*subtitlev1alpha1.SubtitleProfile, error) {
	sorted := make([]*subtitlev1alpha1.SubtitleProfile, 0, len(all))
	for i := range all {
		if !profileInvalid(&all[i]) {
			sorted = append(sorted, &all[i])
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	set := labels.Set(mfLabels)
	for _, p := range sorted {
		if p.Spec.Default || p.Spec.Selector == nil {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil {
			return nil, fmt.Errorf("SubtitleProfile %s: selector: %w", p.Name, err)
		}
		if sel.Matches(set) {
			return p, nil
		}
	}

	var def *subtitlev1alpha1.SubtitleProfile
	for _, p := range sorted {
		if !p.Spec.Default {
			continue
		}
		if def == nil || p.CreationTimestamp.Before(&def.CreationTimestamp) {
			def = p
		}
	}
	return def, nil
}
