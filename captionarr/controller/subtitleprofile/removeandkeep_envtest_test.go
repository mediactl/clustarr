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

package subtitleprofile_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/controller/subtitleprofile"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// An import list's removeAndKeep deletes the Movie and keeps the file and
// its MediaFile record (gap-fix X7b). From a steady state in which the
// profile already ensured the file's request, the next reconcile stops
// counting the kept file, and a kept record the profile had not reached yet
// gets no request at all. Re-adding the Movie makes the file eligible again.
func TestReconcileSkipsAMediaFileWhoseItemIsGone(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "subtitleprofile-removeandkeep"
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	movieFile(t, ctx, c, ns, "arrival-2016", nil)
	sp := defaultProfile(t, ctx, c, "default")
	r := subtitleprofile.NewReconciler(c, k8s.MustNewScheme(), events.NewFakeRecorder(10))
	req := reconcile.Request{NamespacedName: types.NamespacedName{Name: sp.Name}}
	matchingFiles := func() int32 {
		t.Helper()
		var got subtitlev1alpha1.SubtitleProfile
		require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
		return got.Status.MatchingFiles
	}

	_, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	require.Len(t, listRequests(t, ctx, c), 1, "setup: the managed file's request")
	require.EqualValues(t, 1, matchingFiles())

	// removeAndKeep: the Movie goes, its MediaFile stays. A second kept
	// record, never reached before, is a MediaFile with no Movie at all.
	require.NoError(t, c.Delete(ctx, &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "arrival-2016", Namespace: ns}}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: "kept-2001", Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "kept-2001"},
			Path:     "/data/movies/kept-2001/kept-2001.mkv",
		},
	}))

	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.EqualValues(t, 0, matchingFiles(), "neither kept file is the profile's any more")
	requests := listRequests(t, ctx, c)
	require.Len(t, requests, 1, "no request is ensured for a kept record")
	assert.Equal(t, "arrival-2016", requests[0].Name,
		"the existing request is not deleted here; the SubtitleRequest controller blocks it (ItemNotFound)")

	// A list re-adds the film: the kept file is managed again.
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival-2016", Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 329865, QualityProfileRef: "hd", RootFolderRef: "movies"},
	}))
	_, err = r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.EqualValues(t, 1, matchingFiles())
}
