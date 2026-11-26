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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

func lastResortProfile(cutoff string, marks ...bool) *catalogv1alpha1.QualityProfile {
	tiers := []catalogv1alpha1.Tier{
		{Name: "1080p", Qualities: []string{"WEBDL-1080p", "Bluray-1080p"}},
		{Name: "Remux-1080p", Qualities: []string{"Remux-1080p"}},
		{Name: "720p", Qualities: []string{"WEBDL-720p"}},
		{Name: "480p", Qualities: []string{"WEBDL-480p"}},
	}
	for i := range marks {
		tiers[i].LastResort = marks[i]
	}
	return &catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo, Tiers: tiers, Cutoff: cutoff,
	}}
}

func TestALastResortTierIsBelowTheCutoffAndSoIsEveryTierUnderIt(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	q := func(name string) quality.Definition {
		d, ok := quality.Lookup("video", name)
		require.True(t, ok, name)
		return d
	}

	p, errs := quality.FromCRD(lastResortProfile("Remux-1080p", false, false, true, true), cat)
	require.Empty(t, errs)
	require.False(t, p.LastResort(q("Bluray-1080p").Quality))
	require.False(t, p.LastResort(q("Remux-1080p").Quality))
	require.True(t, p.LastResort(q("WEBDL-720p").Quality))
	require.True(t, p.LastResort(q("WEBDL-480p").Quality))
	require.False(t, p.LastResort(q("WEBDL-2160p").Quality), "a quality the profile does not hold is no tier of it")
	require.True(t, p.CutoffMet(q("Remux-1080p").Quality))
	require.False(t, p.CutoffMet(q("WEBDL-720p").Quality))
	require.True(t, p.Outranks(q("Remux-1080p").Quality, q("WEBDL-720p").Quality))
	require.True(t, p.Outranks(q("WEBDL-720p").Quality, q("WEBDL-480p").Quality))
	require.False(t, p.Outranks(q("WEBDL-720p").Quality, q("WEBDL-720p").Quality))
	require.False(t, p.Outranks(q("WEBDL-2160p").Quality, q("WEBDL-480p").Quality))

	plain, errs := quality.FromCRD(lastResortProfile("Remux-1080p"), cat)
	require.Empty(t, errs)
	require.False(t, plain.LastResort(q("WEBDL-720p").Quality))
	require.NotEqual(t, plain.Hash, p.Hash, "the mark is part of what the hash identifies")

	_, errs = quality.FromCRD(lastResortProfile("720p", false, false, true, true), cat)
	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], `tier "720p": a last-resort tier is below the cutoff`)

	_, errs = quality.FromCRD(lastResortProfile("1080p", false, false, true, false), cat)
	require.Len(t, errs, 1)
	require.ErrorContains(t, errs[0], `tier "480p": every tier below a last-resort tier is a last resort too`)
}
