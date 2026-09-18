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
	"time"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/series"
)

func TestEffectiveEpisodeOrder(t *testing.T) {
	cases := []struct {
		name       string
		seriesType catalogv1alpha1.SeriesType
		order      catalogv1alpha1.EpisodeOrder
		want       catalogv1alpha1.EpisodeOrder
	}{
		{"anime forces absolute regardless of spec", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderOfficial, catalogv1alpha1.EpisodeOrderAbsolute},
		{"anime forces absolute even if user set dvd", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderDVD, catalogv1alpha1.EpisodeOrderAbsolute},
		{"standard keeps the spec value", catalogv1alpha1.SeriesTypeStandard, catalogv1alpha1.EpisodeOrderDVD, catalogv1alpha1.EpisodeOrderDVD},
		{"daily keeps official default", catalogv1alpha1.SeriesTypeDaily, catalogv1alpha1.EpisodeOrderOfficial, catalogv1alpha1.EpisodeOrderOfficial},
		{"standard falls back to official on the Go zero value", catalogv1alpha1.SeriesTypeStandard, catalogv1alpha1.EpisodeOrder(""), catalogv1alpha1.EpisodeOrderOfficial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, series.EffectiveEpisodeOrder(c.seriesType, c.order))
		})
	}
}

func TestEpisodeName(t *testing.T) {
	airDate := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		seriesName string
		seriesType catalogv1alpha1.SeriesType
		season     int32
		episode    int32
		airDate    *time.Time
		want       string
	}{
		{"standard", "the-expanse", catalogv1alpha1.SeriesTypeStandard, 2, 5, nil, "the-expanse-s02e05"},
		{"anime uses season/episode too, not absolute", "one-piece", catalogv1alpha1.SeriesTypeAnime, 1, 1092, nil, "one-piece-s01e1092"},
		{"specials season is s00", "the-expanse", catalogv1alpha1.SeriesTypeStandard, 0, 1, nil, "the-expanse-s00e01"},
		{"daily uses the air date", "the-daily-show", catalogv1alpha1.SeriesTypeDaily, 0, 0, &airDate, "the-daily-show-2026-03-04"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, series.EpisodeName(c.seriesName, c.seriesType, c.season, c.episode, c.airDate))
		})
	}
}
