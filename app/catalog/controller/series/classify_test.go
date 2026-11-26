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
	t.Run("under an anime root everything is anime, and nothing is patched", func(t *testing.T) {
		root := rf(nil)
		root.Spec.Kind = catalogv1alpha1.RootFolderKindSeries
		root.Spec.Defaults.SeriesType = catalogv1alpha1.SeriesTypeAnime
		for _, s := range []*catalogv1alpha1.Series{ser("Animation", "Anime"), ser("Drama")} {
			c, patch := series.Classify(s, root)
			require.False(t, patch)
			require.Equal(t, &catalogv1alpha1.SeriesClassification{Anime: true}, c)
		}
		c, _ := series.Classify(&catalogv1alpha1.Series{}, root)
		require.Nil(t, c, "not before its metadata")
	})
	t.Run("an empty seriesType defaults to anime", func(t *testing.T) {
		c, _ := series.Classify(ser("Anime"), rf(&catalogv1alpha1.AnimeDefaults{QualityProfileRef: "p"}))
		require.Equal(t, catalogv1alpha1.SeriesTypeAnime, c.SeriesType)
	})
}

// AnimeRoute is where an anime goes on classification (anime library
// design, 2026-11-25, §2): nowhere when the root folder's anime defaults
// name no folder, the folder they name when it is an anime root, and an
// error -- the series is then not classified at all -- when that folder is
// missing or is a TV root, since a classification recorded without the move
// could never be made again.
func TestAnimeRoute(t *testing.T) {
	root := func(name string, typ catalogv1alpha1.SeriesType, kind catalogv1alpha1.RootFolderKind, route string) *catalogv1alpha1.RootFolder {
		rf := &catalogv1alpha1.RootFolder{Spec: catalogv1alpha1.RootFolderSpec{Kind: kind}}
		rf.Name = name
		rf.Spec.Defaults.SeriesType = typ
		if route != "-" {
			rf.Spec.Defaults.Anime = &catalogv1alpha1.AnimeDefaults{QualityProfileRef: "p", RootFolderRef: route}
		}
		return rf
	}
	kind := catalogv1alpha1.RootFolderKindSeries
	tv := root("tv", catalogv1alpha1.SeriesTypeStandard, kind, "anime")
	anime := root("anime", catalogv1alpha1.SeriesTypeAnime, kind, "-")

	got, err := series.AnimeRoute(tv, anime)
	require.NoError(t, err)
	require.Equal(t, "anime", got)

	for name, rf := range map[string]*catalogv1alpha1.RootFolder{
		"no anime defaults":           root("tv", catalogv1alpha1.SeriesTypeStandard, kind, "-"),
		"anime defaults name no root": root("tv", catalogv1alpha1.SeriesTypeStandard, kind, ""),
	} {
		got, err := series.AnimeRoute(rf, nil)
		require.NoError(t, err, name)
		require.Empty(t, got, name)
	}

	for name, dest := range map[string]*catalogv1alpha1.RootFolder{
		"the folder is missing":      nil,
		"a TV root":                  root("tv2", catalogv1alpha1.SeriesTypeStandard, kind, "-"),
		"a daily root":               root("news", catalogv1alpha1.SeriesTypeDaily, kind, "-"),
		"a movie root, whatever its": root("films", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.RootFolderKindMovie, "-"),
	} {
		_, err := series.AnimeRoute(tv, dest)
		require.ErrorIs(t, err, series.ErrAnimeRoot, name)
	}

	require.True(t, anime.Spec.AnimeRoot())
	require.False(t, tv.Spec.AnimeRoot())
}
