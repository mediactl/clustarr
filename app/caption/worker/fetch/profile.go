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

package fetch

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
)

// resolveProfile returns the SubtitleProfile a request is planned against,
// or nil when none applies.
//
// SubtitleRequestStatus records the profile's generation but not its name,
// so the worker resolves it the way SubtitleRequestSpec.ProfileRef's doc
// comment says the controller does: spec.profileRef when set, else a
// profile whose selector matches the MediaFile's labels, else the default
// profile. A named profile that no longer exists is "none applies", not an
// error: the task predates the profile's deletion and the controller's
// replan supersedes it.
func (w *Worker) resolveProfile(ctx context.Context, ref string, mfLabels map[string]string) (*subtitlev1alpha1.SubtitleProfile, error) {
	if ref != "" {
		var p subtitlev1alpha1.SubtitleProfile
		if err := w.Client.Get(ctx, client.ObjectKey{Name: ref}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, nil
			}
			return nil, fmt.Errorf("get subtitle profile %s: %w", ref, err)
		}
		return &p, nil
	}
	var list subtitlev1alpha1.SubtitleProfileList
	if err := w.Client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list subtitle profiles: %w", err)
	}
	return chooseProfile(list.Items, mfLabels), nil
}

// chooseProfile is resolveProfile's selection rule, pure so it is testable
// without a cluster. Profiles marked Invalid are never chosen. Among
// selector matches the first by name wins; failing any, the oldest default
// wins -- SubtitleProfileSpec.Default's doc comment has the controller mark
// the newer of two defaults Invalid, and preferring the oldest agrees with
// it even before that condition lands.
func chooseProfile(profiles []subtitlev1alpha1.SubtitleProfile, mfLabels map[string]string) *subtitlev1alpha1.SubtitleProfile {
	valid := make([]subtitlev1alpha1.SubtitleProfile, 0, len(profiles))
	for _, p := range profiles {
		if !meta.IsStatusConditionTrue(p.Status.Conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid) {
			valid = append(valid, p)
		}
	}
	slices.SortFunc(valid, func(a, b subtitlev1alpha1.SubtitleProfile) int { return cmp.Compare(a.Name, b.Name) })

	set := labels.Set(mfLabels)
	for i := range valid {
		sel := valid[i].Spec.Selector
		if sel == nil {
			continue
		}
		s, err := metav1.LabelSelectorAsSelector(sel)
		if err != nil || s.Empty() {
			continue
		}
		if s.Matches(set) {
			return &valid[i]
		}
	}

	var def *subtitlev1alpha1.SubtitleProfile
	for i := range valid {
		p := &valid[i]
		if !p.Spec.Default {
			continue
		}
		if def == nil || p.CreationTimestamp.Before(&def.CreationTimestamp) {
			def = p
		}
	}
	return def
}
