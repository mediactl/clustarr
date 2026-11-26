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

package rssmatcher

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/itemread"
)

// TestACandidatesOwnerIsTheRefTheItemPathKeys: the intake parks a candidate
// under the owner the matcher names and wakes that owner's key; the grab
// stage takes the candidates parked under itemread.Ref of the object it is
// planning. A Movie's owner was named "movie" where that ref says "Movie"
// (2026-10-07 to 2026-11-26), so no RSS candidate for a Movie was ever
// taken: each was redelivered until the stream gave up, and the kind e2e's
// firehose scenario waited for a pending grab that could not come.
func TestACandidatesOwnerIsTheRefTheItemPathKeys(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	movie := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "inception", UID: "m-1"}}
	series := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "lost", UID: "s-1"}}
	episode := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "lost-s01e01", UID: "e-1"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "lost", SeasonNumber: 1, EpisodeNumber: 1},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(movie, series, episode).Build()

	st, err := resolve(t.Context(), c, "ns", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "inception"}, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, itemread.Ref(movie), st.owner)

	st, err = resolve(t.Context(), c, "ns", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "lost-s01e01"}, time.Now(), nil)
	require.NoError(t, err)
	require.Equal(t, itemread.Ref(series), st.owner)
}
