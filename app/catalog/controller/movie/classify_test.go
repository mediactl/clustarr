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

package movie_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
)

func TestClassify(t *testing.T) {
	anime := &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "anime-remux-1080p"}
	rf := func(a *catalogv1alpha1.AnimeDefaults) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{Defaults: catalogv1alpha1.RootDefaults{Anime: a}}}
	}
	// TMDB's document for Akira (tmdb 149) as the owner's library holds it.
	mov := func(original string, genres ...string) *catalogv1alpha1.Movie {
		return &catalogv1alpha1.Movie{
			Spec:   catalogv1alpha1.MovieSpec{QualityProfileRef: "hd-bluray-web"},
			Status: catalogv1alpha1.MovieStatus{Metadata: &catalogv1alpha1.MovieMetadata{OriginalLanguage: original, Genres: genres}},
		}
	}
	t.Run("a Japanese animated film is moved", func(t *testing.T) {
		c, patch := movie.Classify(mov("ja", "Animation", "Science Fiction", "Action"), rf(anime))
		require.True(t, patch)
		require.Equal(t, &catalogv1alpha1.MovieClassification{Anime: true, QualityProfileRef: "anime-remux-1080p"}, c)
	})
	t.Run("a film already on the anime profile is recorded without a patch", func(t *testing.T) {
		m := mov("ja", "Animation")
		m.Spec.QualityProfileRef = "anime-remux-1080p"
		c, patch := movie.Classify(m, rf(anime))
		require.False(t, patch)
		require.Equal(t, &catalogv1alpha1.MovieClassification{Anime: true, QualityProfileRef: "anime-remux-1080p"}, c)
	})
	t.Run("an English animated film is not anime", func(t *testing.T) {
		c, patch := movie.Classify(mov("en", "Animation", "Family"), rf(anime))
		require.False(t, patch)
		require.Equal(t, &catalogv1alpha1.MovieClassification{Anime: false}, c)
	})
	t.Run("a Japanese live-action film is not anime", func(t *testing.T) {
		c, patch := movie.Classify(mov("ja", "Drama", "Crime"), rf(anime))
		require.False(t, patch)
		require.Equal(t, &catalogv1alpha1.MovieClassification{Anime: false}, c)
	})
	t.Run("the original language compares as a language", func(t *testing.T) {
		for _, code := range []string{"jpn", "JA", "ja-JP"} {
			_, patch := movie.Classify(mov(code, "Animation"), rf(anime))
			require.True(t, patch, code)
		}
	})
	t.Run("an Anime genre counts, case-insensitively", func(t *testing.T) {
		_, patch := movie.Classify(mov("ja", "anime"), rf(anime))
		require.True(t, patch)
	})
	t.Run("no anime defaults: nothing is recorded", func(t *testing.T) {
		c, patch := movie.Classify(mov("ja", "Animation"), rf(nil))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("already classified: never again", func(t *testing.T) {
		m := mov("ja", "Animation")
		m.Status.Classification = &catalogv1alpha1.MovieClassification{Anime: false}
		c, patch := movie.Classify(m, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("classify off: skipped", func(t *testing.T) {
		m := mov("ja", "Animation")
		m.Annotations = map[string]string{catalogv1alpha1.AnnotationClassify: "off"}
		c, patch := movie.Classify(m, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("no metadata yet: nothing is recorded", func(t *testing.T) {
		c, patch := movie.Classify(&catalogv1alpha1.Movie{}, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
}
