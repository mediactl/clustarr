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

package release

import (
	"strings"
	"testing"
	"time"

	"github.com/dlclark/regexp2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParseSeriesSingleAndMultiEpisode(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		seasons  []int
		episodes []int
	}{
		{"single episode", "Severance.S02E03.Chikhai.Bardo.1080p.ATVP.WEB-DL.DDP5.1.Atmos.H.264-NTb", []int{2}, []int{3}},
		{"dual-token multi", "The.Bear.S03E01E02.720p.HULU.WEB-DL.DDP5.1.H.264-NTb", []int{3}, []int{1, 2}},
		{"dash-range multi", "Fringe.S05E01-E03.1080p.BluRay.x264-DEMAND", []int{5}, []int{1, 2, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseSeries(tt.title, Options{SeriesType: "standard"})
			require.NoError(t, err)
			assert.Equal(t, tt.seasons, p.Seasons)
			assert.Equal(t, tt.episodes, p.Episodes)
			wantType := commonv1.ReleaseTypeSingle
			if len(tt.episodes) > 1 {
				wantType = commonv1.ReleaseTypeMulti
			}
			assert.Equal(t, wantType, p.ReleaseType)
		})
	}
}

func TestParseSeriesSeasonPacks(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		seasons     []int
		fullSeason  bool
		multiSeason bool
		partial     bool
	}{
		{"full season", "Fringe.S05.1080p.BluRay.x264-DEMAND", []int{5}, true, false, false},
		{"multi-season", "Game.of.Thrones.S01-S03.1080p.BluRay.x264-ROVERS", []int{1, 2, 3}, true, true, false},
		{"partial season", "Some.Show.S01.Part2.720p.WEB-DL.x264-GROUP", []int{1}, false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseSeries(tt.title, Options{SeriesType: "standard"})
			require.NoError(t, err)
			assert.Equal(t, tt.seasons, p.Seasons)
			assert.Equal(t, tt.fullSeason, p.FullSeason)
			assert.Equal(t, tt.multiSeason, p.MultiSeason)
			assert.Equal(t, tt.partial, p.Partial)
			if tt.fullSeason {
				assert.Equal(t, commonv1.ReleaseTypeSeasonPack, p.ReleaseType)
			}
		})
	}
}

// TestParseSeriesCascadePropagatesRegexTimeoutInsteadOfFallingThrough forces
// a genuine regexp2 MatchTimeout inside the standard-family stage (by
// temporarily swapping seasonOnlyRegex for a classic catastrophic-
// backtracking pattern with a tiny timeout) and asserts that parseSeries's
// standard->daily->anime fallback cascade (tv.go) returns that timeout
// immediately rather than treating it as "no match" and trying the next
// family.
func TestParseSeriesCascadePropagatesRegexTimeoutInsteadOfFallingThrough(t *testing.T) {
	original := seasonOnlyRegex
	pathological := regexp2.MustCompile(`(a+)+$`, regexp2.None)
	pathological.MatchTimeout = 1 * time.Millisecond
	seasonOnlyRegex = pathological
	defer func() { seasonOnlyRegex = original }()

	// No "S\d+E\d+"/"S\d+" token, so dashRangeEpisodeRegex and
	// multiEpisodeRegex both cleanly report "no match" first, leaving
	// seasonOnlyRegex (now pathological) as the one that actually runs.
	title := "Show." + strings.Repeat("a", 30) + "!.720p.HDTV-GROUP"

	_, err := parseSeries(title, Options{})
	require.Error(t, err)
	assert.True(t, isRegexTimeout(err), "expected a regexp2 match-timeout error, got: %v", err)
}
