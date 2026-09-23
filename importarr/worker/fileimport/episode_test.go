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

package fileimport_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestMatchEpisodes(t *testing.T) {
	cands := []fileimport.EpisodeCandidate{
		{Name: "s01e01", Season: 1, Number: 1, Absolute: 1, AirDate: "2008-01-20"},
		{Name: "s01e02", Season: 1, Number: 2, Absolute: 2, AirDate: "2008-01-27"},
		{Name: "s01e03", Season: 1, Number: 3, Absolute: 3, AirDate: "2008-02-10"},
		{
			Name: "s02e01", Season: 2, Number: 1, Absolute: 8, AirDate: "2009-03-08",
			SceneSeason: ptr.To[int32](1), SceneNumber: ptr.To[int32](8),
		},
		{Name: "special", Season: 0, Number: 1, AirDate: "2008-02-10"},
	}
	for _, tc := range []struct {
		file       string
		seriesType catalogv1alpha1.SeriesType
		want       []string
		reason     string
	}{
		{file: "Show.S01E03.1080p.WEB-DL-GRP.mkv", want: []string{"s01e03"}},
		{file: "Show (2008) - S01E01E02 - Pilot [WEBDL-1080p].mkv", want: []string{"s01e01", "s01e02"}},
		{file: "Show.S00E01.1080p.WEB-DL-GRP.mkv", want: []string{"special"}},
		{file: "Show.S01E08.1080p.WEB-DL-GRP.mkv", want: []string{"s02e01"}},
		{file: "Show.S01E09.1080p.WEB-DL-GRP.mkv", reason: "the series has no S01E09"},
		{file: "Show.2008.01.27.720p.HDTV.x264-GRP.mkv", want: []string{"s01e02"}},
		{file: "Show.2008.02.10.720p.HDTV.x264-GRP.mkv", reason: "2 episodes of the series aired on 2008-02-10"},
		{file: "[SubsPlease] Show - 08 (1080p) [ABCD1234].mkv", seriesType: catalogv1alpha1.SeriesTypeAnime, want: []string{"s02e01"}},
		{
			file: "[SubsPlease] Show - 09 (1080p) [ABCD1234].mkv", seriesType: catalogv1alpha1.SeriesTypeAnime,
			reason: "the series has no absolute episode 9",
		},
	} {
		t.Run(tc.file, func(t *testing.T) {
			p, err := release.ParsePath("/dl/"+tc.file, release.Options{Kind: commonv1.MediaKindEpisode})
			require.NoError(t, err)
			eps, reason := fileimport.MatchEpisodes(p, tc.seriesType, cands)
			if tc.reason != "" {
				assert.Nil(t, eps)
				assert.Contains(t, reason, tc.reason)
				return
			}
			require.Empty(t, reason)
			var got []string
			for _, e := range eps {
				got = append(got, e.Name)
			}
			assert.Equal(t, tc.want, got)
		})
	}

	two := []fileimport.EpisodeCandidate{{Name: "a", Season: 1, Number: 1}, {Name: "b", Season: 1, Number: 2}}
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "a"}, fileimport.EpisodeFileRef(two[:1]))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "a", Keys: []string{"a", "b"}},
		fileimport.EpisodeFileRef(two), "a multi-episode file names every episode it holds")
}
