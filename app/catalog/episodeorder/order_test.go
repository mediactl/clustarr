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

package episodeorder_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/episodeorder"
)

func TestEffectiveEpisodeOrder(t *testing.T) {
	cases := []struct {
		name       string
		seriesType catalogv1alpha1.SeriesType
		order      catalogv1alpha1.EpisodeOrder
		want       catalogv1alpha1.EpisodeOrder
	}{
		{"anime keeps the official order", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderOfficial, catalogv1alpha1.EpisodeOrderOfficial},
		{"anime keeps an explicit dvd order", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderDVD, catalogv1alpha1.EpisodeOrderDVD},
		{"anime keeps an explicit absolute order", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrderAbsolute, catalogv1alpha1.EpisodeOrderAbsolute},
		{"anime falls back to official on the Go zero value", catalogv1alpha1.SeriesTypeAnime, catalogv1alpha1.EpisodeOrder(""), catalogv1alpha1.EpisodeOrderOfficial},
		{"standard keeps the spec value", catalogv1alpha1.SeriesTypeStandard, catalogv1alpha1.EpisodeOrderDVD, catalogv1alpha1.EpisodeOrderDVD},
		{"daily keeps official default", catalogv1alpha1.SeriesTypeDaily, catalogv1alpha1.EpisodeOrderOfficial, catalogv1alpha1.EpisodeOrderOfficial},
		{"standard falls back to official on the Go zero value", catalogv1alpha1.SeriesTypeStandard, catalogv1alpha1.EpisodeOrder(""), catalogv1alpha1.EpisodeOrderOfficial},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, episodeorder.EffectiveEpisodeOrder(c.seriesType, c.order))
		})
	}
}
