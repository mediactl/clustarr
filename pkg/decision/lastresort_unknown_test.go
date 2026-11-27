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

package decision_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// A profile takes a release whose title names no quality by listing
// Unknown in a last-resort tier (owner, 2026-11-27: "tentatively allow
// "unknown" quality releases - but we should find out the quality after
// download to determine if we'll keep it"): the release is approved, its
// tier is a last resort, which the grab planner takes only when nothing
// better covers the episode, and the import judges the file by its probe
// (quality.ResolveUnknown). The profile is built from its manifest as talos
// builds it: the tier as first rolled held nothing, since the table's
// Unknown names no source and a parsed one names an unknown one.
func TestAReleaseNamingNoQualityIsALastResortUnderAProfileThatListsUnknown(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	profile := func(unknown bool) quality.Profile {
		spec := catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Tiers: []catalogv1alpha1.Tier{
				{Name: "1080p", Qualities: []string{"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"}},
				{Name: "Remux-1080p", Qualities: []string{"Remux-1080p"}},
				{Name: "720p", Qualities: []string{"WEBDL-720p", "WEBRip-720p", "HDTV-720p", "Bluray-720p"}, LastResort: true},
			},
			Cutoff:         "Remux-1080p",
			ScoreSet:       "anime-sonarr",
			Audio:          &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true, CompleteFirst: true},
			SizeTable:      catalogv1alpha1.SizeTableAnime,
			PreferSmallest: true,
		}
		if unknown {
			spec.Tiers = append(spec.Tiers, catalogv1alpha1.Tier{Name: "Unknown", Qualities: []string{"Unknown"}, LastResort: true})
		}
		p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: spec}, cat)
		require.Empty(t, errs)
		return p
	}
	const (
		noQuality = "Frieren.S01E02.AAC2.0-GRP"
		hd720     = "Frieren.S01E02.720p.WEB-DL.AAC2.0.H.264-GRP"
	)
	judge := func(p quality.Profile, title string) decision.Decision {
		ds := decision.Evaluate(context.Background(), frierenEpisode(), p, cat, []common.ReleaseInfo{lrRelease(title)}, usenetOnly)
		require.Len(t, ds, 1)
		return ds[0]
	}

	with := profile(true)
	d := judge(with, noQuality)
	require.Equal(t, "Unknown", d.Release.Quality.Name)
	assert.True(t, d.Approved, "%v", reasons(d))
	assert.True(t, with.LastResort(d.Release.Quality), "it is taken only when nothing better exists")
	assert.True(t, with.Outranks(judge(with, hd720).Release.Quality, d.Release.Quality), "a 720p release, itself a last resort, is a better one")
	assert.False(t, with.CutoffMet(d.Release.Quality))

	// What the import makes of it: an unknown source at the probed
	// resolution stays in the tier until the probe names it.
	sized, _ := quality.AugmentFromMediaInfo(d.Release.Quality, &common.MediaInfo{Width: 1920, Height: 1080})
	assert.True(t, with.Allowed(sized))
	assert.True(t, with.LastResort(sized))

	without := profile(false)
	d = judge(without, noQuality)
	assert.False(t, d.Approved)
	assert.True(t, hasRejection(d, decision.ReasonQualityNotWanted), "%v", reasons(d))
	assert.False(t, without.Allowed(sized))
}
