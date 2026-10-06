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

package series_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
)

func TestClassify(t *testing.T) {
	anime := &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "anime-web-1080p", SeriesType: catalogv1alpha1.SeriesTypeAnime}
	rf := func(a *catalogv1alpha1.AnimeDefaults) *catalogv1alpha1.RootFolder {
		return &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{Defaults: catalogv1alpha1.RootDefaults{Anime: a}}}
	}
	ser := func(genres ...string) *catalogv1alpha1.Series {
		return &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{Genres: genres}}}
	}
	t.Run("an anime series is moved", func(t *testing.T) {
		c, patch := series.Classify(ser("Animation", "Anime"), rf(anime))
		require.True(t, patch)
		require.Equal(t, &catalogv1alpha1.SeriesClassification{Anime: true, QualityProfileRef: "anime-web-1080p", SeriesType: catalogv1alpha1.SeriesTypeAnime}, c)
	})
	t.Run("a non-anime series is recorded, not moved", func(t *testing.T) {
		c, patch := series.Classify(ser("Drama"), rf(anime))
		require.False(t, patch)
		require.Equal(t, &catalogv1alpha1.SeriesClassification{Anime: false}, c)
	})
	t.Run("the genre matches case-insensitively", func(t *testing.T) {
		_, patch := series.Classify(ser("anime"), rf(anime))
		require.True(t, patch)
	})
	t.Run("no anime defaults: nothing is recorded", func(t *testing.T) {
		c, patch := series.Classify(ser("Anime"), rf(nil))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("already classified: never again", func(t *testing.T) {
		s := ser("Anime")
		s.Status.Classification = &catalogv1alpha1.SeriesClassification{Anime: false}
		c, patch := series.Classify(s, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("classify off: skipped", func(t *testing.T) {
		s := ser("Anime")
		s.Annotations = map[string]string{catalogv1alpha1.AnnotationClassify: "off"}
		c, patch := series.Classify(s, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("no metadata yet: nothing is recorded", func(t *testing.T) {
		c, patch := series.Classify(&catalogv1alpha1.Series{}, rf(anime))
		require.Nil(t, c)
		require.False(t, patch)
	})
	t.Run("an empty seriesType defaults to anime", func(t *testing.T) {
		c, _ := series.Classify(ser("Anime"), rf(&catalogv1alpha1.AnimeDefaults{QualityProfileRef: "p"}))
		require.Equal(t, catalogv1alpha1.SeriesTypeAnime, c.SeriesType)
	})
}
