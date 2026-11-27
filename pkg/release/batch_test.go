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

// A title that names a series and nothing else is rewritten as that
// series' season pack, and only on an exact title. The titles are the ones
// two season searches on talos returned on 2026-11-27, for Rozen Maiden:
// Zurückspulen and Yozakura Quartet: Hana no Uta, every one of them
// unparseable.
func TestSeriesBatchTitle(t *testing.T) {
	rozen := []string{"Rozen Maiden - Zurückspulen", "ローゼンメイデン（2013）", "Rozen Maiden - Rewind"}
	yozakura := []string{"Yozakura Quartet (2013)", "夜桜四重奏 ～ハナノウタ～", "Yozakura Quartet: Hana no Uta"}
	for _, tc := range []struct {
		title  string
		titles []string
		want   string
	}{
		{
			"[GERMini] Rozen Maiden 2013 Zurückspulen [BD.1080p.x265.HEVC][Dual Audio]", rozen,
			"[GERMini] Rozen Maiden - Zurückspulen S01 [BD.1080p.x265.HEVC][Dual Audio]",
		},
		{
			"[Zaphod] Rozen Maiden: Zurückspulen (2013) [BDRip 1920x1080 x264 Hi10P FLAC]", rozen,
			"[Zaphod] Rozen Maiden - Zurückspulen S01 (2013) [BDRip 1920x1080 x264 Hi10P FLAC]",
		},
		// A usenet post that dropped the umlaut, dotted.
		{
			"[GERMini].Rozen.Maiden.2013.Zurckspulen.[BD.1080p.x265.HEVC][Jap.Eng.DubEng.Sub]", rozen,
			"[GERMini]. Rozen Maiden - Zurückspulen S01 [BD.1080p.x265.HEVC][Jap.Eng.DubEng.Sub]",
		},
		{"Yozakura Quartet - Hana no Uta", yozakura, "Yozakura Quartet: Hana no Uta S01"},
		// Less than the series, more than the series, another series.
		{"Rozen Maiden (2013)", rozen, ""},
		{"[Group] Rozen Maiden Zurückspulen Movie [1080p]", rozen, ""},
		{"[Group] Rozen Maiden Zurückspulen - 01 [1080p]", rozen, ""},
		{"Rozen.Maiden.Zurückspulen.1080p.BluRay.x265-GRP", rozen, ""},
		{"[Group] Some Other Show [BD 1080p]", rozen, ""},
		{"[Group] [BD 1080p]", rozen, ""},
	} {
		got := SeriesBatchTitle(tc.title, tc.titles, 1)
		assert.Equal(t, tc.want, got, tc.title)
		if got == "" {
			continue
		}
		p, err := Parse(got, Options{Kind: commonv1.MediaKindSeries})
		require.NoError(t, err, got)
		assert.True(t, p.FullSeason, got)
		assert.Equal(t, []int{1}, p.Seasons, got)
	}
	assert.Empty(t, SeriesBatchTitle("[GERMini] Rozen Maiden 2013 Zurückspulen [BD.1080p]", rozen, 0), "the specials are no season to be a pack of")
	assert.Equal(t, "[G] Rozen Maiden - Zurückspulen S03 [BD]", SeriesBatchTitle("[G] Rozen Maiden Zurückspulen [BD]", rozen, 3))
}
