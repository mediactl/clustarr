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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/release"
)

// TestAFileCountingThroughTheSeriesUnderSeasonOne: the file of a release
// named "One.Piece.S01E1157" -- a streaming service's one season for the
// whole run -- is the episode whose absolute number is 1157, TVDB's S23E02,
// as the search read its release for the grab (pkg/decision's
// countedThrough). Read as written it named an episode the series does not
// have, in the importer and the scanner alike (2026-11-28).
func TestAFileCountingThroughTheSeriesUnderSeasonOne(t *testing.T) {
	cands := []fileimport.EpisodeCandidate{
		{Name: "special", Season: 0, Number: 39, Absolute: 590},
		{Name: "s01e01", Season: 1, Number: 1, Absolute: 1},
		{Name: "s01e08", Season: 1, Number: 8, Absolute: 8},
		{Name: "s02e01", Season: 2, Number: 1, Absolute: 9},
		{Name: "s16e11", Season: 16, Number: 11, Absolute: 589},
		{Name: "s23e01", Season: 23, Number: 1, Absolute: 1156},
		{Name: "s23e02", Season: 23, Number: 2, Absolute: 1157},
		{Name: "s23e03", Season: 23, Number: 3, Absolute: 1158},
		// Two regular episodes under one number: nothing says which.
		{Name: "s22e69", Season: 22, Number: 69, Absolute: 1154},
		{Name: "s22e70", Season: 22, Number: 70, Absolute: 1154},
	}
	var numbering decision.Identity
	for _, c := range cands {
		if c.Season >= 1 {
			numbering.Numbering = append(numbering.Numbering, decision.EpisodeNumbering{Season: c.Season, Episode: c.Number, Absolute: c.Absolute})
		}
	}
	const file = "One.Piece.S01E1157.Nami.in.a.Fix.An.Adventure.in.Block.Kingdom.1080p.CR.WEB-DL.DUAL.AAC2.0.H.264-VARYG.mkv"
	anime := catalogv1alpha1.SeriesTypeAnime

	for _, tc := range []struct {
		name       string
		file       string
		seriesType catalogv1alpha1.SeriesType
		want       []string
		reason     string
	}{
		{name: "the episode holding the number", file: file, seriesType: anime, want: []string{"s23e02"}},
		{name: "a number season 1 has is season 1's", file: "One.Piece.S01E08.1080p.CR.WEB-DL-VARYG.mkv", seriesType: anime, want: []string{"s01e08"}},
		{name: "a number past season 1", file: "One.Piece.S01E09.1080p.CR.WEB-DL-VARYG.mkv", seriesType: anime, want: []string{"s02e01"}},
		{name: "two numbers, one in season 1", file: "One.Piece.S01E08E09.1080p.CR.WEB-DL-VARYG.mkv", seriesType: anime, want: []string{"s01e08", "s02e01"}},
		{name: "only an anime counts so", file: file, reason: "the series has no S01E1157"},
		{name: "another season's number is as written", file: "One.Piece.S02E1157.1080p.CR.WEB-DL-VARYG.mkv", seriesType: anime, reason: "the series has no S02E1157"},
		{name: "a number no episode holds", file: "One.Piece.S01E1300.1080p.CR.WEB-DL-VARYG.mkv", seriesType: anime, reason: "the series has no S01E1300"},
		{name: "a special's number is not in the count", file: "One.Piece.S01E590.1080p.CR.WEB-DL-VARYG.mkv", seriesType: anime, reason: "the series has no S01E590"},
		{name: "a number two episodes hold", file: "One.Piece.S01E1154.1080p.CR.WEB-DL-VARYG.mkv", seriesType: anime, reason: "the series has no S01E1154"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := release.ParsePath("/dl/"+tc.file, release.Options{Kind: commonv1.MediaKindEpisode})
			require.NoError(t, err)
			for reader, match := range map[string]func() ([]fileimport.EpisodeCandidate, string){
				"the scanner": func() ([]fileimport.EpisodeCandidate, string) {
					return fileimport.MatchEpisodes(p, tc.seriesType, cands)
				},
				"the importer": func() ([]fileimport.EpisodeCandidate, string) {
					id := decision.Identity{}
					if tc.seriesType == anime {
						id = numbering
					}
					return fileimport.MatchDownloaded(p, tc.seriesType, cands, id)
				},
			} {
				eps, reason := match()
				if tc.reason != "" {
					assert.Nil(t, eps, reader)
					assert.Contains(t, reason, tc.reason, reader)
					continue
				}
				require.Empty(t, reason, reader)
				var got []string
				for _, e := range eps {
					got = append(got, e.Name)
				}
				assert.Equal(t, tc.want, got, reader)
			}
		})
	}
}
