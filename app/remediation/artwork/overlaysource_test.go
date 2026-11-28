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

package artwork

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A profile's edit that changes what the compact rendition is drawn from
// and nothing the regular one is -- spec.compactGeometry -- wakes the
// namespace's Movies and Series, whose overlay stage then asks for the
// render (the compact-posters design, 2026-11-27). The OverlayProfile
// controller cannot: it compares status.overlay, which the compact
// rendition has no place in, so before this source such an edit redrew
// nothing until each item happened to be woken for another reason. An edit
// the regular poster sees is the controller's to publish for, and its
// render task draws both renditions.
func TestAProfileEditOfTheCompactGeometryAloneWakesItsItems(t *testing.T) {
	i32 := func(v int32) *int32 { return &v }
	base := &catalogv1alpha1.OverlayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "metascore", Namespace: "films"},
		Spec:       catalogv1alpha1.OverlayProfileSpec{Corner: catalogv1alpha1.OverlayCornerTopLeft},
	}
	edit := func(f func(*catalogv1alpha1.OverlayProfileSpec)) *catalogv1alpha1.OverlayProfile {
		p := base.DeepCopy()
		f(&p.Spec)
		return p
	}
	for _, tc := range []struct {
		name  string
		after *catalogv1alpha1.OverlayProfile
		wakes bool
	}{
		{"the compact width", edit(func(s *catalogv1alpha1.OverlayProfileSpec) {
			s.CompactGeometry = &catalogv1alpha1.OverlayGeometry{WidthPercent: i32(33)}
		}), true},
		{"a compact geometry that says what the default does", edit(func(s *catalogv1alpha1.OverlayProfileSpec) {
			s.CompactGeometry = &catalogv1alpha1.OverlayGeometry{WidthPercent: i32(catalogv1alpha1.DefaultOverlayCompactWidthPercent)}
		}), false},
		{"the corner: the regular poster's too, so the controller's", edit(func(s *catalogv1alpha1.OverlayProfileSpec) {
			s.Corner = catalogv1alpha1.OverlayCornerBottomLeft
		}), false},
		{"the regular width, which the compact one carries when it is the larger", edit(func(s *catalogv1alpha1.OverlayProfileSpec) {
			s.Geometry = &catalogv1alpha1.OverlayGeometry{WidthPercent: i32(40)}
		}), false},
		{"the selector", edit(func(s *catalogv1alpha1.OverlayProfileSpec) {
			s.Selector = &metav1.LabelSelector{MatchLabels: map[string]string{"a": "b"}}
		}), false},
		{"nothing", base.DeepCopy(), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.wakes, compactAloneChanged(base, tc.after))
		})
	}
	assert.False(t, compactAloneChanged(base, &catalogv1alpha1.Movie{}), "another kind is not a profile")

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "films"}},
		&catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "lost", Namespace: "films"}},
		&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "other"}},
	).Build()
	var _ client.Reader = c
	keys := overlaidItems(context.Background(), c, "films")
	require.ElementsMatch(t, []remediation.Key{
		{Kind: remediation.KindMovie, Namespace: "films", Name: "heat"},
		{Kind: remediation.KindSeries, Namespace: "films", Name: "lost"},
	}, keys, "the namespace's Movies and Series, the two kinds with an overlay")

	var w remediation.Watcher = NewOverlay(Options{})
	assert.NotNil(t, w, "the overlay stage declares the source")
}
