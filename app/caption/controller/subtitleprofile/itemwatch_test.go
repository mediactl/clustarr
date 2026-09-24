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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A Movie or Episode appearing or going changes whether its kept MediaFile
// is selected (itemSet.manages) without touching the MediaFile, so the item
// watch maps it onto the profiles that could win that file: every default
// plus every selector match -- and nothing for an item no file references.
func TestItemWatchMapsOntoTheProfilesThatCouldWinItsFile(t *testing.T) {
	def := profileAt("default", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{Default: true})
	hd := profileAt("hd", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "hd"}},
	})
	other := profileAt("other", time.Now(), subtitlev1alpha1.SubtitleProfileSpec{
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "sd"}},
	})
	file := movieFile("arrival-2016", map[string]string{"tier": "hd"})
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(&def, &hd, &other, file).Build()
	r := NewReconciler(c, k8s.MustNewScheme(), nil)

	names := func(o *catalogv1alpha1.Movie) []string {
		var out []string
		for _, req := range r.mapItemToProfiles(context.Background(), o) {
			out = append(out, req.Name)
		}
		return out
	}
	movie := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "arrival-2016", Namespace: "media"}}
	assert.ElementsMatch(t, []string{"default", "hd"}, names(movie))
	assert.Empty(t, names(&catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "unfiled", Namespace: "media"}}))
	assert.Empty(t, r.mapItemToProfiles(context.Background(),
		&catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Name: "arrival-2016", Namespace: "media"}}),
		"an Episode of the same name is not this movie file's item")

	p := createdOrDeleted()
	assert.True(t, p.Create(event.CreateEvent{Object: movie}))
	assert.True(t, p.Delete(event.DeleteEvent{Object: movie}))
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: movie, ObjectNew: movie}),
		"catalogarr's frequent Movie status writes do not replan every profile")
}
