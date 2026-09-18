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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestParseSeriesAnimeAbsoluteAndBracketGroup(t *testing.T) {
	tests := []struct {
		name        string
		title       string
		seriesTitle string
		absolute    []int
		group       string
	}{
		// quality.md §7.3: rls drops the group entirely here.
		{
			"subsplease absolute", "[SubsPlease] Frieren - 28 (1080p) [F02B9CDC].mkv",
			"Frieren",
			[]int{28},
			"SubsPlease",
		},
		// quality.md §7.3: rls mis-sets group to "ARA" (a language token) here.
		{
			"erai-raws absolute", "[Erai-raws] One Piece - 1090 [1080p][Multiple Subtitle].mkv",
			"One Piece",
			[]int{1090},
			"Erai-raws",
		},
		{
			"season plus absolute", "[SubsPlease] Attack on Titan - S04E28 (1080p) [ABCD1234].mkv",
			"Attack on Titan", nil, "SubsPlease",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseSeries(tt.title, Options{SeriesType: "anime"})
			require.NoError(t, err)
			assert.Equal(t, tt.seriesTitle, p.Title)
			assert.Equal(t, tt.absolute, p.Absolute)
			assert.Equal(t, tt.group, p.Group)
		})
	}
}

func TestParseSeriesAnimeOVASpecialToken(t *testing.T) {
	p, err := parseSeries("[SubsPlease] My Hero Academia - OVA 01 (1080p) [HASH].mkv", Options{SeriesType: "anime"})
	require.NoError(t, err)
	assert.Equal(t, "My Hero Academia", p.Title)
	assert.Equal(t, "SubsPlease", p.Group)
	assert.True(t, p.Special)
}

// TestParseSeriesAnimeOVAPrefixWithoutWordBoundaryIsNotASpecial is the
// regression the missing \b in animeSpecialRegex caused: "OVAN" is not the
// word "OVA", so it must not flip Special to true, and the episode number
// that follows it must still come through as an absolute episode via the
// normal fallback.
func TestParseSeriesAnimeOVAPrefixWithoutWordBoundaryIsNotASpecial(t *testing.T) {
	p, err := parseSeries("[SubsPlease] Show - OVAN 01 (1080p) [HASH].mkv", Options{SeriesType: "anime"})
	require.NoError(t, err)
	assert.False(t, p.Special)
	assert.Equal(t, []int{1}, p.Absolute)
}

func TestParseSeriesAnimeBatchRangesExpandInclusive(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		absolute []int
	}{
		{"dash range", "[SubsPlease] Frieren - 01-12 (1080p) [HASH].mkv", intRange(1, 12)},
		{"tilde range", "[SubsPlease] Frieren - 01~12 (1080p) [HASH].mkv", intRange(1, 12)},
		{"paren-wrapped range", "[SubsPlease] Frieren - (01-24) [HASH].mkv", intRange(1, 24)},
		{"spaced dash range", "[SubsPlease] Frieren - 01 - 12 (1080p) [HASH].mkv", intRange(1, 12)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseSeries(tt.title, Options{SeriesType: "anime"})
			require.NoError(t, err)
			assert.Equal(t, "Frieren", p.Title)
			assert.Equal(t, tt.absolute, p.Absolute)
			assert.True(t, p.Partial)
			assert.Equal(t, commonv1.ReleaseTypeMulti, p.ReleaseType)
		})
	}
}

func TestParseSeriesAnimeBatchRangeRejectsDescendingAndOversized(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		absolute []int
	}{
		// Descending: treated as not a batch at all, falling back to the
		// single-absolute interpretation (just the first number).
		{"descending range", "[SubsPlease] Frieren - 12-01 (1080p) [HASH].mkv", []int{12}},
		// 900 episodes (001-900) exceeds the 500-episode sanity cap.
		{"oversized range", "[SubsPlease] Frieren - 001-900 (1080p) [HASH].mkv", []int{1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseSeries(tt.title, Options{SeriesType: "anime"})
			require.NoError(t, err)
			assert.Equal(t, tt.absolute, p.Absolute)
			assert.False(t, p.Partial)
			assert.Equal(t, commonv1.ReleaseTypeSingle, p.ReleaseType)
		})
	}
}
