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

package quality_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestProfileIndexAllowedAndCutoffMet(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl1080, _ := quality.Lookup("video", "WEBDL-1080p")
	webrip1080, _ := quality.Lookup("video", "WEBRip-1080p")
	bluray720, _ := quality.Lookup("video", "Bluray-720p")
	p := quality.Profile{
		Tiers:       [][]quality.Definition{{bluray1080}, {webdl1080, webrip1080}, {bluray720}},
		CutoffIndex: 0,
	}

	idx, ok := p.Index(bluray1080.Quality)
	require.True(t, ok)
	require.Equal(t, 0, idx)

	idx, ok = p.Index(webrip1080.Quality) // second tier, tied with webdl1080
	require.True(t, ok)
	require.Equal(t, 1, idx)

	require.True(t, p.Allowed(bluray720.Quality))
	require.False(t, p.Allowed(common.Quality{Source: common.SourceCam, Modifier: common.ModifierNone})) // not in any tier

	require.True(t, p.CutoffMet(bluray1080.Quality)) // at cutoff tier
	require.False(t, p.CutoffMet(webdl1080.Quality))  // below cutoff (higher index)
}

func TestIndexAllowedCutoffMetOnZeroValueProfileDoNotPanic(t *testing.T) {
	var p quality.Profile // nil Tiers, nil Scores, nil Sizes
	_, ok := p.Index(common.Quality{Source: common.SourceBluray})
	require.False(t, ok)
	require.False(t, p.Allowed(common.Quality{}))
	require.False(t, p.CutoffMet(common.Quality{}))
}
