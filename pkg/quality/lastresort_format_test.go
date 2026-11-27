/*
Copyright (C) 2026 The Clustarr Authors

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

	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// A profile names the custom formats it takes only as a last resort
// (owner, 2026-11-27: "low-quality-group encodes should be allowed as a
// last resort when nothing else exists").
func TestAProfileNamesTheFormatsItTakesAsALastResort(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	crd := lastResortProfile("Remux-1080p")
	crd.Spec.ScoreSet = "anime-sonarr"
	plain, errs := quality.FromCRD(crd, cat)
	require.Empty(t, errs)
	require.Less(t, plain.Scores["anime-lq-groups"], 0, "the catalogue scores the groups below zero")
	require.False(t, plain.LastResortFormat([]string{"anime-lq-groups"}))
	require.Equal(t, -700, plain.LastResortScore(-700, []string{"anime-lq-groups", "x265-hd"}), "an unmarked format gives nothing back")

	crd.Spec.LastResortFormats = []string{"anime-lq-groups"}
	p, errs := quality.FromCRD(crd, cat)
	require.Empty(t, errs)
	require.True(t, p.LastResortFormat([]string{"x265-hd", "anime-lq-groups"}))
	require.False(t, p.LastResortFormat([]string{"x265-hd", "anime-raws"}))
	lq := p.Scores["anime-lq-groups"]
	require.Equal(t, 300, p.LastResortScore(lq+300, []string{"anime-lq-groups", "x265-hd"}), "the score without what the marked format took")
	require.Equal(t, -9000, p.LastResortScore(-9000, []string{"anime-raws"}), "another format's score stands")
	require.NotEqual(t, plain.Hash, p.Hash, "the list is part of what the hash identifies")

	crd.Spec.LastResortFormats = []string{"no-such-format"}
	_, errs = quality.FromCRD(crd, cat)
	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], `lastResortFormats references unknown format "no-such-format"`)
}
