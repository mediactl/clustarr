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

package fetch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

func profileWith(langs ...subtitlev1alpha1.LanguageItem) *subtitlev1alpha1.SubtitleProfile {
	return &subtitlev1alpha1.SubtitleProfile{Spec: subtitlev1alpha1.SubtitleProfileSpec{Languages: langs}}
}

func TestWantForResolvesTheEntryAndItsAliases(t *testing.T) {
	p := profileWith(
		subtitlev1alpha1.LanguageItem{Key: "pt-BR", Language: "pt-BR", HI: subtitlev1alpha1.HIPolicyPrefer},
		subtitlev1alpha1.LanguageItem{Key: "en:forced", Language: "en", Forced: true},
	)
	p.Spec.LanguageEquals = []string{"pt-BR:pt"}

	w, ok := wantFor(p, nil, "pt-BR")
	require.True(t, ok)
	assert.Equal(t, []string{"pt", "pt-BR"}, w.tags(), "languageEquals makes pt acceptable for pt-BR")

	w, ok = wantFor(p, nil, "en:forced")
	require.True(t, ok)
	assert.True(t, w.forced)
	assert.Equal(t, []string{"en"}, w.tags())

	_, ok = wantFor(p, nil, "fr")
	assert.False(t, ok, "a key the profile does not have is a stale task")
	_, ok = wantFor(p, []string{"en:forced"}, "pt-BR")
	assert.False(t, ok, "spec.languages excluding the key is a stale task")
}

func TestReject(t *testing.T) {
	enPrefer := want{lang: "en", hi: subtitlev1alpha1.HIPolicyPrefer, accept: map[string]bool{"en": true}}
	enRequired := enPrefer
	enRequired.hi = subtitlev1alpha1.HIPolicyRequired
	enExcluded := enPrefer
	enExcluded.hi = subtitlev1alpha1.HIPolicyExcluded
	enForced := enPrefer
	enForced.forced = true

	f, err := compileFilters(subtitlev1alpha1.SubtitleProfileSpec{
		MustContain: []string{`bluray|web`}, MustNotContain: []string{`(?<!non-)hc\b`},
	})
	require.NoError(t, err)
	none := filters{}

	cases := []struct {
		name     string
		c        subtitles.Candidate
		w        want
		f        filters
		verified bool
		rejected bool
	}{
		{"ffprobe's eng matches en", subtitles.Candidate{Language: "eng"}, enPrefer, none, true, false},
		{"und is never guessed", subtitles.Candidate{Language: "und"}, enPrefer, none, true, true},
		{"another language", subtitles.Candidate{Language: "fr"}, enPrefer, none, true, true},
		{"forced for a non-forced key", subtitles.Candidate{Language: "en", Forced: true}, enPrefer, none, true, true},
		{"non-forced for a forced key", subtitles.Candidate{Language: "en"}, enForced, none, true, true},
		{"HI required, verified non-HI", subtitles.Candidate{Language: "en"}, enRequired, none, true, true},
		{"HI required, unverifiable flag", subtitles.Candidate{Language: "en"}, enRequired, none, false, false},
		{"HI excluded, verified HI", subtitles.Candidate{Language: "en", HI: true}, enExcluded, none, true, true},
		{"prefer takes HI", subtitles.Candidate{Language: "en", HI: true}, enPrefer, none, true, false},
		{"machine translated", subtitles.Candidate{Language: "en", MachineTranslated: true}, enPrefer, none, true, true},
		{
			"AI translated allowed by option",
			subtitles.Candidate{Language: "en", AITranslated: true},
			enPrefer,
			none.withProviderOptions(map[string]string{"aiTranslated": "include"}), true, false,
		},
		{
			"untrusted under trustedSources",
			subtitles.Candidate{Language: "en"},
			enPrefer,
			none.withProviderOptions(map[string]string{"trustedSources": "true"}), true, true,
		},
		{"mustContain met, case-insensitive", subtitles.Candidate{Language: "en", ReleaseInfo: "Film.2010.BluRay"}, enPrefer, f, true, false},
		{"mustContain missed", subtitles.Candidate{Language: "en", ReleaseInfo: "Film.2010.HDTV"}, enPrefer, f, true, true},
		{"mustNotContain hit (lookbehind, regexp2 only)", subtitles.Candidate{Language: "en", ReleaseInfo: "Film.WEB.HC"}, enPrefer, f, true, true},
		{"mustNotContain lookbehind spares non-hc", subtitles.Candidate{Language: "en", ReleaseInfo: "Film.WEB.non-HC"}, enPrefer, f, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reject(tc.c, tc.w, tc.f, tc.verified)
			assert.Equal(t, tc.rejected, got != "", "reason %q", got)
		})
	}
}

func TestIdentityMatchesAndSearchable(t *testing.T) {
	os, gd := subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, subtitlev1alpha1.SubtitleProviderGestdown
	movieByID := subtitles.Query{IDs: map[string]string{"imdb": "133093"}}
	hashOnly := subtitles.Query{IDs: map[string]string{}, Hash: "8e245d9679d31e12"}
	bare := subtitles.Query{IDs: map[string]string{}}
	episodeByTVDB := subtitles.Query{IDs: map[string]string{"tvdb": "81189", "parent_imdb": "903747"}}

	assert.Equal(t, map[string]bool{subtitles.MatchTitle: true, subtitles.MatchYear: true},
		identityMatches(os, commonv1.MediaKindMovie, movieByID))
	assert.Nil(t, identityMatches(os, commonv1.MediaKindEpisode, episodeByTVDB),
		"the OpenSubtitles client ignores parent ids, so an episode search is not pinned to the show")
	assert.Len(t, identityMatches(gd, commonv1.MediaKindEpisode, episodeByTVDB), 4)
	assert.Nil(t, identityMatches(gd, commonv1.MediaKindEpisode, bare))

	assert.True(t, searchable(os, commonv1.MediaKindMovie, movieByID))
	assert.True(t, searchable(os, commonv1.MediaKindMovie, hashOnly))
	assert.False(t, searchable(os, commonv1.MediaKindMovie, bare), "a bare language filter returns other titles")
	assert.False(t, searchable(os, commonv1.MediaKindEpisode, episodeByTVDB))
	assert.True(t, searchable(os, commonv1.MediaKindEpisode, hashOnly))
	assert.True(t, searchable(gd, commonv1.MediaKindEpisode, episodeByTVDB))
	assert.False(t, searchable(gd, commonv1.MediaKindEpisode, hashOnly))
	assert.True(t, searchable(subtitlev1alpha1.SubtitleProviderEmbedded, commonv1.MediaKindMovie, bare))
}

// rank must reproduce Bazarr's numbers: an OpenSubtitles movie searched by
// imdb id gets title+year (100) and the release-derived matches on top; a
// hash match survives only when the release corroborates it.
func TestRankScoresFiltersAndOrders(t *testing.T) {
	target, err := release.Parse("Film.2010.1080p.BluRay.x264-GRP", release.Options{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	in := rankInput{
		kind:           commonv1.MediaKindMovie,
		providerType:   subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom,
		hashVerifiable: true,
		hiVerifiable:   true,
		query:          subtitles.Query{Kind: commonv1.MediaKindMovie, IDs: map[string]string{"imdb": "1"}, Release: target},
		want:           want{lang: "en", hi: subtitlev1alpha1.HIPolicyPrefer, accept: map[string]bool{"en": true}},
		threshold:      subtitles.MinScore(commonv1.MediaKindMovie, 70), // 126
	}
	cands := []subtitles.Candidate{
		{ID: "web", Language: "en", ReleaseInfo: "Film.2010.720p.WEB-DL.x264-OTHER"},  // 100+1 codec
		{ID: "exact", Language: "en", ReleaseInfo: "Film.2010.1080p.BluRay.x264-GRP"}, // 100+30+15+1+1
		{ID: "hash", Language: "en", ReleaseInfo: "Film.2010.1080p.BluRay.x264-GRP", Downloads: 1, // hash corroborated
			Matches: map[string]bool{subtitles.MatchHash: true}},
		{ID: "badhash", Language: "en", ReleaseInfo: "Film.2010.HDTV", Matches: map[string]bool{subtitles.MatchHash: true}},
		{ID: "french", Language: "fr", ReleaseInfo: "Film.2010.1080p.BluRay.x264-GRP"},
	}

	rr := rank(in, cands)
	require.Len(t, rr.accepted, 2)
	assert.Equal(t, "hash", rr.accepted[0].c.ID)
	assert.Equal(t, 179, rr.accepted[0].score, "a corroborated hash collapses to the hash weight alone")
	assert.Equal(t, "exact", rr.accepted[1].c.ID)
	assert.Equal(t, 147, rr.accepted[1].score)
	assert.Equal(t, 1, rr.filtered, "the French candidate")
	assert.Equal(t, 101, rr.bestBelow, "the WEB-DL release and the uncorroborated hash both miss 126")
}

func TestDecide(t *testing.T) {
	someChosen := &chosen{}
	for _, tc := range []struct {
		name     string
		out      searchOutcome
		eligible int
		onDisk   bool
		want     verdict
	}{
		{"downloaded", searchOutcome{chosen: someChosen}, 1, false, verdictDownloaded},
		{"upgrade downloaded", searchOutcome{chosen: someChosen}, 1, true, verdictDownloaded},
		{"upgrade found nothing better", searchOutcome{searched: 2}, 2, true, verdictKeep},
		{"upgrade with every provider throttled", searchOutcome{throttled: []string{"a"}}, 1, true, verdictKeep},
		{"no provider can serve it", searchOutcome{}, 0, false, verdictUnavailable},
		{"answered, nothing good enough", searchOutcome{searched: 1, bestBelow: 90}, 1, false, verdictUnavailable},
		{"acceptable but undownloadable", searchOutcome{searched: 1, acceptable: 2, fetchErrors: []string{"x"}}, 1, false, verdictFailed},
		{"one answered, one errored", searchOutcome{searched: 1, providerErrors: []string{"x"}}, 2, false, verdictUnavailable},
		{"every provider errored", searchOutcome{providerErrors: []string{"x", "y"}}, 2, false, verdictFailed},
		{"errored and throttled", searchOutcome{providerErrors: []string{"x"}, throttled: []string{"y"}}, 2, false, verdictFailed},
		{"throttled everywhere", searchOutcome{throttled: []string{"a", "b"}}, 2, false, verdictThrottled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, decide(tc.out, tc.eligible, tc.onDisk))
		})
	}
}

func TestMinScore(t *testing.T) {
	pct := subtitlev1alpha1.ScorePct{Movie: 70, Episode: 90}
	ov := int32(50)
	onDisk := &subtitlev1alpha1.SubtitleItem{Score: 150}

	assert.Equal(t, 126, minScore(commonv1.MediaKindMovie, pct, nil, 0, nil, false))
	assert.Equal(t, 324, minScore(commonv1.MediaKindEpisode, pct, nil, 0, nil, false))
	assert.Equal(t, 90, minScore(commonv1.MediaKindMovie, pct, &ov, 0, nil, false), "minScoreOverride wins")
	assert.Equal(t, 140, minScore(commonv1.MediaKindMovie, pct, nil, 140, nil, false), "the upgrade pass's score+1")
	assert.Equal(t, 151, minScore(commonv1.MediaKindMovie, pct, nil, 0, onDisk, true), "never replace with worse or equal")
}

func TestChooseProfile(t *testing.T) {
	old := metav1.NewTime(time.Unix(1000, 0))
	newer := metav1.NewTime(time.Unix(2000, 0))
	mk := func(name string, def bool, created metav1.Time, sel map[string]string, invalid bool) subtitlev1alpha1.SubtitleProfile {
		p := subtitlev1alpha1.SubtitleProfile{ObjectMeta: metav1.ObjectMeta{Name: name, CreationTimestamp: created}}
		p.Spec.Default = def
		if sel != nil {
			p.Spec.Selector = &metav1.LabelSelector{MatchLabels: sel}
		}
		if invalid {
			p.Status.Conditions = []metav1.Condition{{Type: subtitlev1alpha1.SubtitleProfileConditionInvalid, Status: metav1.ConditionTrue}}
		}
		return p
	}
	profiles := []subtitlev1alpha1.SubtitleProfile{
		mk("default-new", true, newer, nil, false),
		mk("default-old", true, old, nil, false),
		mk("anime", false, old, map[string]string{"genre": "anime"}, false),
		mk("broken", false, old, map[string]string{"genre": "docs"}, true),
	}

	assert.Equal(t, "anime", chooseProfile(profiles, map[string]string{"genre": "anime"}).Name)
	assert.Equal(t, "default-old", chooseProfile(profiles, map[string]string{"genre": "docs"}).Name,
		"an Invalid profile is never chosen; the oldest default is")
	assert.Nil(t, chooseProfile(profiles[2:3], nil), "no match and no default: none applies")
}

func TestLocalPathAndIMDbNumber(t *testing.T) {
	got, err := localPath("/mnt/media", "/data/movies/Film (2010)/Film.mkv")
	require.NoError(t, err)
	assert.Equal(t, "/mnt/media/movies/Film (2010)/Film.mkv", got)
	_, err = localPath("/mnt/media", "/data/../etc/passwd")
	assert.Error(t, err)
	_, err = localPath("/mnt/media", "movies/x.mkv")
	assert.Error(t, err)

	assert.Equal(t, "133093", imdbNumber("tt0133093"))
	assert.Equal(t, "", imdbNumber("nm0000206x"))
	assert.Equal(t, "", imdbNumber(""))
}
