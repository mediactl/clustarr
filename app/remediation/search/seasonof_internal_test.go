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

package search

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// An Episode reads its season's PackFirst test from its Series'
// status.seasons, with no season search state at all, beside the pending
// and answered inputs it read before (owner, 2026-10-27).
func TestSeasonOfReadsItsSeasonsPackFirstTest(t *testing.T) {
	ser := &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{Seasons: []catalogv1alpha1.SeasonStatus{
		{Number: 1, Monitored: true, EpisodeCount: 7},
		{Number: 2, Monitored: true, EpisodeCount: 7, EpisodeFileCount: 6},
	}}}
	ep := func(season int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: season, EpisodeNumber: 3}}
	}
	pending, answered, packFirst := seasonOf(ser, ep(1), nil)
	require.False(t, pending)
	require.Nil(t, answered)
	require.True(t, packFirst)
	_, _, packFirst = seasonOf(ser, ep(2), nil)
	require.False(t, packFirst, "one missing episode is searched as an episode")

	ser.Status.SeasonSearch = &catalogv1alpha1.SeasonSearchState{Seasons: []catalogv1alpha1.SeasonSearchSeason{
		{Number: 1, Phase: catalogv1alpha1.SeasonSearchQueued},
	}}
	pending, _, packFirst = seasonOf(ser, ep(1), nil)
	require.True(t, pending)
	require.True(t, packFirst)
}

// An Episode keeps waiting for its season's pack query while a single of
// the season downloads: a pack is grabbed over the single and replaces it
// (owner, 2026-11-03, reversing 2026-11-02's "no pack while an episode
// downloads"). Without an audio policy taking complete audio first no grab
// holds the season (TestOnlyACompleteGrabHoldsItsSeasonUnderCompleteFirst),
// and a donor entry is no video grab at all.
func TestSeasonOfStaysPackFirstWhileASingleDownloads(t *testing.T) {
	ser := &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{Seasons: []catalogv1alpha1.SeasonStatus{
		{Number: 1, Monitored: true, EpisodeCount: 12},
		{Number: 2, Monitored: true, EpisodeCount: 12},
	}}}
	ep := func(season int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: season, EpisodeNumber: 3}}
	}
	entry := func(phase commonv1.DownloadPhase, purpose commonv1.DownloadPurpose) catalogv1alpha1.DownloadEntry {
		return catalogv1alpha1.DownloadEntry{
			ID: "e", UID: "u", Phase: phase, Purpose: purpose,
			Episodes: []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 7}},
		}
	}
	for _, tc := range []struct {
		name      string
		entries   []catalogv1alpha1.DownloadEntry
		packFirst bool
	}{
		{"nothing downloading", nil, true},
		{"a single of the season downloading", []catalogv1alpha1.DownloadEntry{entry(commonv1.DownloadPhaseDownloading, "")}, true},
		{"its grab imported", []catalogv1alpha1.DownloadEntry{entry(commonv1.DownloadPhaseImported, "")}, true},
		{"an audio donor downloading", []catalogv1alpha1.DownloadEntry{entry(commonv1.DownloadPhaseDownloading, commonv1.DownloadPurposeAudioDonor)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ser.DeepCopy()
			s.Status.Downloads = tc.entries
			_, _, packFirst := seasonOf(s, ep(1), nil)
			require.Equal(t, tc.packFirst, packFirst)
			_, _, other := seasonOf(s, ep(2), nil)
			require.True(t, other, "another season's grab holds no other season")
		})
	}
}

// Only a grab carrying every wanted language, under a profile taking
// complete audio first, holds its season: a pack without them is refused
// beside it ("All, unless they outrank its audio", owner, 2026-11-03), and
// any other pack is grabbed over the single (2026-11-02: Remake our Life!'s
// EMBER pack had waited on SENPAI's Japanese-only singles).
func TestOnlyACompleteGrabHoldsItsSeasonUnderCompleteFirst(t *testing.T) {
	first := &quality.Profile{AudioLanguages: []string{"en", "original"}, GraftAudio: true, AudioCompleteFirst: true}
	ser := &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{
		Metadata: &catalogv1alpha1.SeriesMetadata{OriginalLanguage: "ja"},
		Seasons:  []catalogv1alpha1.SeasonStatus{{Number: 1, Monitored: true, EpisodeCount: 12}},
	}}
	ep := &catalogv1alpha1.Episode{Spec: catalogv1alpha1.EpisodeSpec{SeasonNumber: 1, EpisodeNumber: 3}}
	single := func(languages ...string) catalogv1alpha1.DownloadEntry {
		return catalogv1alpha1.DownloadEntry{
			ID: "e", UID: "u", Phase: commonv1.DownloadPhaseDownloading,
			Episodes: []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 7}},
			Release:  catalogv1alpha1.DownloadRelease{Languages: languages},
		}
	}
	for _, tc := range []struct {
		name      string
		prof      *quality.Profile
		languages []string
		packFirst bool
	}{
		{"a Japanese-only single downloading", first, []string{"Japanese"}, true},
		{"a dual-audio single downloading", first, []string{"Japanese", "English"}, false},
		{"a Japanese-only single without completeFirst", &quality.Profile{AudioLanguages: []string{"en", "original"}, GraftAudio: true}, []string{"Japanese"}, true},
		{"a dual-audio single without completeFirst", &quality.Profile{AudioLanguages: []string{"en", "original"}, GraftAudio: true}, []string{"Japanese", "English"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ser.DeepCopy()
			s.Status.Downloads = []catalogv1alpha1.DownloadEntry{single(tc.languages...)}
			_, _, packFirst := seasonOf(s, ep, tc.prof)
			require.Equal(t, tc.packFirst, packFirst)
		})
	}
}
