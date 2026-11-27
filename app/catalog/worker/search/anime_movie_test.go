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

package search

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// A film classified anime is searched as an anime request, so the indexers
// kept to anime (Indexer.spec.animeOnly) are asked for it; any other film's
// request is not anime, and they are not.
func TestAnAnimeFilmIsSearchedAsAnime(t *testing.T) {
	const ns = "media"
	for _, tc := range []struct {
		name           string
		classification *catalogv1alpha1.MovieClassification
		want           bool
	}{
		{name: "classified anime", classification: &catalogv1alpha1.MovieClassification{Anime: true}, want: true},
		{name: "classified not anime", classification: &catalogv1alpha1.MovieClassification{}},
		{name: "not classified yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &catalogv1alpha1.Movie{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "akira-1988"},
				Spec:       catalogv1alpha1.MovieSpec{TmdbID: 149, QualityProfileRef: "anime", RootFolderRef: "movies"},
				Status: catalogv1alpha1.MovieStatus{
					Metadata:       &catalogv1alpha1.MovieMetadata{Title: "Akira", Year: 1988, OriginalLanguage: "ja"},
					Classification: tc.classification,
				},
			}
			w := &Worker{Client: fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(m).Build()}
			snap, err := w.snapshot(context.Background(), ns, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name})
			require.NoError(t, err)

			req := BuildSearchRequest(ns, commonv1.MediaKindMovie, snap.IDs, 100, false, nil, nil)
			require.Equal(t, tc.want, req.Anime)
			require.Equal(t, "Akira 1988", req.Text, "the film's text is its title and year either way")
			require.False(t, snap.Target.Anime, "the decision's episode-count rule is a series'")
		})
	}
}
