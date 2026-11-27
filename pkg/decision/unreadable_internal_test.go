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

package decision

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

func reasons(rejs []common.Rejection) []string {
	out := make([]string, 0, len(rejs))
	for _, r := range rejs {
		for _, known := range []Reason{ReasonTranscodedFinal, ReasonPackDownloading, ReasonQueueHigherPreference, ReasonUnableToParse, ReasonExistingHigherPreference} {
			if known.Of(r) {
				out = append(out, known.Code)
			}
		}
	}
	return out
}

// A release whose title cannot be read is held to the item's state all the
// same, by what its search read it as; one read as nothing is refused.
// Until 2026-11-27 a failed read answered "no rejection", so the release
// was held to no item-state rule: on talos, while the manager stood at its
// CPU limit and reads timed out, seven weekly singles were taken over
// transcoded files of As a Reincarnated Aristocrat's third season and some
// twenty-five German singles of Date A Live's first beside two packs
// still downloading and then over the files those packs had brought.
func TestAReleaseWhoseTitleCannotBeReadIsStillHeldToTheItemsState(t *testing.T) {
	saved := parseTitle
	t.Cleanup(func() { parseTitle = saved })
	parseTitle = func(title string, _ release.Options) (*release.ParsedRelease, error) {
		return nil, errors.New("match timeout after 50ms on input `" + title + "`")
	}

	cat := catalogue.LoadedCatalogue()
	// talos' hevc-small-1080p-anime-tv, where it happened.
	p, errs := quality.FromCRD(&catalogv1alpha1.QualityProfile{Spec: catalogv1alpha1.QualityProfileSpec{
		MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
		Tiers: []catalogv1alpha1.Tier{
			{Name: "1080p", Qualities: []string{"WEBDL-1080p", "WEBRip-1080p", "HDTV-1080p", "Bluray-1080p"}},
			{Name: "Remux-1080p", Qualities: []string{"Remux-1080p"}},
		},
		Cutoff:   "Remux-1080p",
		ScoreSet: "anime-sonarr",
		Audio:    &catalogv1alpha1.AudioPolicy{Languages: []string{"en", "original"}, Graft: true, CompleteFirst: true},
	}}, cat)
	require.Empty(t, errs)
	lookup := func(name string) common.Quality {
		d, ok := quality.Lookup("video", name)
		require.True(t, ok, name)
		return d.Quality
	}
	hd, web := lookup("HDTV-1080p"), lookup("WEBDL-1080p")
	_, listed := p.Index(hd)
	require.True(t, listed, "the profile takes the release's quality")
	rel := common.ReleaseInfo{
		Title:   "[Judas] Tensei Kizoku, Kantei Skill de Nariagaru - S03E02 [1080p][HEVC x265 10bit][Multi-Subs] (Weekly)",
		Quality: hd, ReleaseType: common.ReleaseTypeSingle, Languages: []string{"Japanese"},
	}
	final := &Current{Quality: web, Transcoded: true, AudioLanguages: []string{"ja", "en"}}

	t.Run("a file that carries every wanted language refuses it", func(t *testing.T) {
		// Episodes 1 to 7: transcoded, Japanese and English.
		got := ItemStateRejections(context.Background(), Target{Kind: common.MediaKindEpisode, OriginalLanguageTag: "ja", Current: final}, p, Decision{Release: rel}, Options{})
		assert.Equal(t, []string{ReasonExistingHigherPreference.Code}, reasons(got))
	})
	t.Run("a file that is final refuses it", func(t *testing.T) {
		// Episode 9: transcoded, Japanese alone, and the release is no
		// dual audio that would replace it.
		lacking := &Current{Quality: web, Transcoded: true, AudioLanguages: []string{"ja"}}
		got := ItemStateRejections(context.Background(), Target{Kind: common.MediaKindEpisode, OriginalLanguageTag: "ja", Current: lacking}, p, Decision{Release: rel}, Options{})
		assert.Contains(t, reasons(got), ReasonTranscodedFinal.Code)
	})
	t.Run("a pack still downloading holds the episode against it", func(t *testing.T) {
		queue := []Queued{{Candidate: quality.Candidate{Quality: web}, Pack: true, Languages: []string{"Japanese"}}}
		got := ItemStateRejections(context.Background(), Target{Kind: common.MediaKindEpisode, OriginalLanguageTag: "ja", Queue: queue}, p, Decision{Release: rel}, Options{})
		assert.Contains(t, reasons(got), ReasonPackDownloading.Code)
	})
	t.Run("a release read as nothing is refused", func(t *testing.T) {
		bare := rel
		bare.Quality = common.Quality{}
		got := ItemStateRejections(context.Background(), Target{Kind: common.MediaKindEpisode, OriginalLanguageTag: "ja", Current: final}, p, Decision{Release: bare}, Options{})
		require.Len(t, got, 1)
		assert.True(t, ReasonUnableToParse.Of(got[0]))
	})
	t.Run("what the search read it as is what it is judged by", func(t *testing.T) {
		pr, err := ParseForItemState(context.Background(), common.MediaKindEpisode, "ja", rel)
		require.NoError(t, err)
		assert.Equal(t, hd, pr.Quality)
		assert.Equal(t, []string{"Japanese"}, pr.Languages)
		assert.False(t, pr.FullSeason)
		pack := rel
		pack.ReleaseType = common.ReleaseTypeSeasonPack
		pr, err = ParseForItemState(context.Background(), common.MediaKindEpisode, "ja", pack)
		require.NoError(t, err)
		assert.True(t, pr.FullSeason)
	})
}

// A pack is a pack by its title, whatever it was taken for.
func TestPackTitle(t *testing.T) {
	for title, want := range map[string]bool{
		"[Judas] Aharen-san wa Hakarenai (Aharen Is Indecipherable) (Season 2) [1080p][HEVC x265 10bit][Dual-Audio][Multi-Subs] (Batch)": true,
		"Aharen-san.wa.Hakarenai.S02.1080p.BluRay.Dual-Audio.Opus.2.0.x265-YURASUKA":                                                     true,
		"[Erai-raws] Date A Live - 01 ~ 12 [1080p CR WEBRip HEVC AAC][MultiSub] [BATCH]":                                                 true,
		"[Judas] Aharen-san wa Hakarenai (Aharen Is Indecipherable) - S02E07 [1080p][HEVC x265 10bit][Multi-Subs] (Weekly)":              false,
		"Show.S01E03.1080p.WEB.H264-GRP": false,
		"":                               false,
	} {
		assert.Equal(t, want, PackTitle(title), title)
	}
}
