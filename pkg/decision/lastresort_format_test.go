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

package decision_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// animeLowQualityGroups is animeLastResort with the anime low-quality
// groups named a last resort, or not.
func animeLowQualityGroups(t *testing.T, lastResort bool) quality.Profile {
	t.Helper()
	spec := catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{
			{Name: "1080p", Qualities: []string{"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"}},
			{Name: "Remux-1080p", Qualities: []string{"Remux-1080p"}},
		},
		Cutoff:         "Remux-1080p",
		FormatScores:   []catalogv1alpha1.FormatScore{{Format: "x265-hd", Score: 300}},
		ScoreSet:       "anime-sonarr",
		Audio:          &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true, CompleteFirst: true},
		SizeTable:      catalogv1alpha1.SizeTableAnime,
		PreferSmallest: true,
	}
	if lastResort {
		spec.LastResortFormats = []string{"anime-lq-groups"}
	}
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: spec}, catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	return p
}

// A release only its low-quality group puts below the profile's minimum is
// approved when the profile takes that format as a last resort, and the
// grab planner decides whether anything better exists. On talos
// (2026-11-27) whole seasons had no other release: Altair, Yuuki Yuuna's
// third, Sacrificial Princess, every candidate refused for its group. A
// release below the minimum for another format too stays refused.
func TestAReleaseOnlyItsLastResortFormatPutsBelowTheMinimumIsApproved(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	const (
		lowQuality = "Frieren.S01E02.1080p.WEB-DL.AAC2.0.HEVC.x265-ASW"
		raw        = "Frieren.S01E02.1080p.BluRay.FLAC2.0.HEVC.x265-Moozzi2"
	)
	judge := func(lastResort bool, title string) decision.Decision {
		ds := decision.Evaluate(context.Background(), frierenEpisode(), animeLowQualityGroups(t, lastResort), cat,
			[]common.ReleaseInfo{lrRelease(title)}, usenetOnly)
		require.Len(t, ds, 1)
		return ds[0]
	}

	d := judge(true, lowQuality)
	require.Contains(t, d.Release.MatchedFormats, "anime-lq-groups")
	require.Less(t, d.Release.FormatScore, int32(0), "its score is unchanged, so it still ranks under every other release")
	require.True(t, d.Approved, "%v", reasons(d))

	d = judge(false, lowQuality)
	require.False(t, d.Approved)
	require.True(t, belowMinimum(d), "%v", reasons(d))

	d = judge(true, raw)
	require.Contains(t, d.Release.MatchedFormats, "anime-raws")
	require.False(t, d.Approved, "a raw is below the minimum by a format the profile does not take")
	require.True(t, belowMinimum(d), "%v", reasons(d))
}

// belowMinimum reports a decision refused for its custom format score.
func belowMinimum(d decision.Decision) bool {
	for _, r := range reasons(d) {
		if strings.HasPrefix(r, "CustomFormatMinimumScore") {
			return true
		}
	}
	return false
}
