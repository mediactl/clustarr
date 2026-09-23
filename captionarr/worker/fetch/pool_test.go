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
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/providerset"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// stubProvider is the least subtitles.Provider eligible needs: a name and
// capabilities.
type stubProvider struct {
	name string
	caps subtitles.Capabilities
}

func (p stubProvider) Name() string                         { return p.name }
func (p stubProvider) Capabilities() subtitles.Capabilities { return p.caps }
func (p stubProvider) HIVerifiable() bool                   { return true }
func (p stubProvider) Search(context.Context, subtitles.Query) ([]subtitles.Candidate, error) {
	return nil, nil
}

func (p stubProvider) Download(context.Context, subtitles.Candidate) ([]byte, string, error) {
	return nil, "", nil
}

var movieCaps = subtitles.Capabilities{Movies: true}

func remoteEntry(name string, typ subtitlev1alpha1.SubtitleProviderType, c subtitles.Provider) providerset.Entry {
	return providerset.Entry{Name: name, Type: typ, Client: c}
}

func localEntry(name string, c subtitles.Provider) providerset.Entry {
	return providerset.Entry{
		Name: name, Type: subtitlev1alpha1.SubtitleProviderEmbedded,
		ForFile: func(providerset.FileSource) subtitles.Provider { return c },
	}
}

// R-4: every SubtitleProvider is judged on its own. Two accounts of one
// type -- whose clients share a Name() -- are both eligible; the
// pkg/subtitles.Registry this replaced kept only the first.
func TestEligibleKeepsEveryAccountOfOneType(t *testing.T) {
	os := subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom
	en := want{lang: "en", accept: map[string]bool{"en": true}}
	q := subtitles.Query{IDs: map[string]string{"imdb": "1"}}
	entries := []providerset.Entry{
		remoteEntry("os-main", os, stubProvider{name: "opensubtitlescom", caps: movieCaps}),
		remoteEntry("os-spare", os, stubProvider{name: "opensubtitlescom", caps: movieCaps}),
		remoteEntry("tv-only", subtitlev1alpha1.SubtitleProviderSubSource, stubProvider{name: "subsource", caps: subtitles.Capabilities{Episodes: true}}),
		remoteEntry("no-forced", subtitlev1alpha1.SubtitleProviderSubDL, stubProvider{name: "subdl", caps: movieCaps}),
		localEntry("local", stubProvider{name: "embedded", caps: subtitles.Capabilities{Movies: true, ForcedSearch: true}}),
	}

	got, skipped := eligible(entries, providerset.FileSource{}, commonv1.MediaKindMovie, en, q, false)
	var names []string
	for _, ep := range got {
		names = append(names, ep.entry.Name)
	}
	assert.Equal(t, []string{"os-main", "os-spare", "no-forced"}, names, "both accounts, in priority order")
	assert.ElementsMatch(t, []string{
		"tv-only: does not serve movie subtitles",
		"local: embedded extraction is off or the file is not probed yet",
	}, skipped)

	forced := en
	forced.forced = true
	got, _ = eligible(entries, providerset.FileSource{}, commonv1.MediaKindMovie, forced, q, true)
	names = nil
	for _, ep := range got {
		names = append(names, ep.entry.Name)
	}
	assert.Equal(t, []string{"local"}, names, "only a client that can search forced subtitles is asked for them")
}

func TestTiersPutLocalProvidersFirst(t *testing.T) {
	ps := []eligibleProvider{
		{entry: remoteEntry("os", subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, nil)},
		{entry: localEntry("local", nil)},
		{entry: remoteEntry("gd", subtitlev1alpha1.SubtitleProviderGestdown, nil)},
	}
	assert.Equal(t, [][]int{{1}, {0, 2}}, tiers(ps))
	assert.Equal(t, [][]int{{0}}, tiers(ps[:1]), "an empty tier is left out")
	assert.Empty(t, tiers(nil))
}

// The pool is Bazarr's download_best_subtitles order over every provider's
// candidates at once: score, then score without the hash, then the
// providers' own priority order, then downloads and id.
func TestRankPoolOrdersEveryProvidersCandidatesTogether(t *testing.T) {
	cand := func(id string, downloads int) subtitles.Candidate {
		return subtitles.Candidate{ID: id, Downloads: downloads}
	}
	pool := []pooled{
		{provider: 0, r: ranked{c: cand("first-weak", 0), score: 132, without: 132}},
		{provider: 0, r: ranked{c: cand("first-tie", 0), score: 147, without: 147}},
		{provider: 1, r: ranked{c: cand("second-best", 0), score: 160, without: 160}},
		{provider: 1, r: ranked{c: cand("second-tie", 99), score: 147, without: 147}},
		{provider: 2, r: ranked{c: cand("third-hash", 0), score: 147, without: 40}},
		{provider: 0, r: ranked{c: cand("first-tie-b", 5), score: 147, without: 147}},
	}
	rankPool(pool)
	var ids []string
	for _, pc := range pool {
		ids = append(ids, pc.r.c.ID)
	}
	assert.Equal(t, []string{
		"second-best", // a lower-priority provider's better subtitle wins
		"first-tie-b", // ties go to priority, then downloads
		"first-tie",
		"second-tie",
		"third-hash", // equal score, less of it without the hash
		"first-weak",
	}, ids)
}

func TestSidecarModeComesFromTheContainingRootFolder(t *testing.T) {
	root := func(path, mode string) catalogv1alpha1.RootFolder {
		var rf catalogv1alpha1.RootFolder
		rf.Spec.Path, rf.Spec.Permissions.FileMode = path, mode
		return rf
	}
	roots := []catalogv1alpha1.RootFolder{
		root("/data/media/movies", "0640"),
		root("/data/media/movies/kids", "0644"),
		root("/data/media/tv/", "0600"),
		root("/data/media/broken", "rw-r--r--"),
		root("/data/media/unset", ""),
	}
	for _, tc := range []struct {
		path     string
		fallback os.FileMode
		want     os.FileMode
	}{
		{"/data/media/movies/Film (2010)/Film (2010).mkv", 0, 0o640},
		{"/data/media/movies/kids/Cartoon (2001)/Cartoon (2001).mkv", 0, 0o644},
		{"/data/media/tv/Show/Season 01/Show - S01E01.mkv", 0, 0o600},
		{"/data/media/moviesextra/Film (2010).mkv", 0, DefaultSidecarMode},
		{"/data/media/broken/Film.mkv", 0, DefaultSidecarMode},
		{"/data/media/unset/Film.mkv", 0o660, 0o660},
		{"/data/elsewhere/Film.mkv", 0o600, 0o600},
	} {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, sidecarMode(roots, tc.path, tc.fallback))
		})
	}

	m, ok := parseFileMode("0775")
	require.True(t, ok)
	assert.Equal(t, os.FileMode(0o775), m)
	for _, bad := range []string{"", "775", "0778", "00775", "0x75"} {
		_, ok := parseFileMode(bad)
		assert.False(t, ok, bad)
	}
}
