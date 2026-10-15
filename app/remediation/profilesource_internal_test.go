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

package remediation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestAQualityProfileWakesItsFilesOnlyWhenItsLanguagePolicyChanges holds
// the predicate of S10' (languagePolicyChanged): a profile that stops
// grafting, changes its default, its audio languages or its language wakes
// its files; any other edit, and a profile the cache's initial list
// delivers, wakes none.
func TestAQualityProfileWakesItsFilesOnlyWhenItsLanguagePolicyChanges(t *testing.T) {
	qp := func(mutate func(*catalogv1alpha1.QualityProfileSpec)) *catalogv1alpha1.QualityProfile {
		p := &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{Cutoff: "hd",
			Audio: &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true, Default: "en"}}}
		if mutate != nil {
			mutate(&p.Spec)
		}
		return p
	}
	pred := languagePolicyChanged()
	for _, tc := range []struct {
		name   string
		mutate func(*catalogv1alpha1.QualityProfileSpec)
		want   bool
	}{
		{"stops grafting", func(s *catalogv1alpha1.QualityProfileSpec) { s.Audio.Graft = false }, true},
		{"another default", func(s *catalogv1alpha1.QualityProfileSpec) { s.Audio.Default = "original" }, true},
		{"other languages", func(s *catalogv1alpha1.QualityProfileSpec) { s.Audio.Languages = []string{"en"} }, true},
		{"no audio policy", func(s *catalogv1alpha1.QualityProfileSpec) { s.Audio = nil }, true},
		{"another language", func(s *catalogv1alpha1.QualityProfileSpec) { s.Language = "en" }, true},
		{"another cutoff", func(s *catalogv1alpha1.QualityProfileSpec) { s.Cutoff = "uhd" }, false},
	} {
		got := pred.Update(event.UpdateEvent{ObjectOld: qp(nil), ObjectNew: qp(tc.mutate)})
		assert.Equal(t, tc.want, got, tc.name)
	}
	assert.True(t, pred.Delete(event.DeleteEvent{Object: qp(nil)}))
	assert.True(t, pred.Create(event.CreateEvent{Object: qp(nil)}), "a profile created later: its items' files may hold blocks")
	assert.False(t, pred.Create(event.CreateEvent{Object: qp(nil), IsInInitialList: true}), "the files' own initial list wakes them")
}
