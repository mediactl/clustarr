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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// loadReleases reads testdata/decision/releases.json the same way
// pkg/quality/trash_corpus_test.go reads testdata/trash: os.ReadFile with a
// filepath.Join("..", "..", "testdata", ...) relative path, not go:embed --
// an embed pattern cannot contain ".." (it may only reach files inside its
// own package directory), so it cannot see a repo-root testdata/ directory
// from pkg/decision/.
func loadReleases(t *testing.T) []common.ReleaseInfo {
	t.Helper()
	doc, err := os.ReadFile(filepath.Join("..", "..", "testdata", "decision", "releases.json"))
	require.NoError(t, err)
	var rels []common.ReleaseInfo
	require.NoError(t, json.Unmarshal(doc, &rels))
	return rels
}

func TestEvaluateFullPipeline(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{
		Tiers:                 [][]quality.Definition{{bluray1080}, {webdl720}},
		CutoffIndex:           0,
		UpgradeAllowed:        true,
		CutoffFormatScore:     10000,
		MinUpgradeFormatScore: 1,
		ProperPolicy:          "preferAndUpgrade",
		LanguageName:          "any",
		Sizes:                 quality.MovieSizeTable(),
		PreferredProtocol:     "torrent",
	}
	tg := decision.Target{
		Kind:                common.MediaKindMovie,
		Available:           true,
		OriginalLanguageTag: "en",
		// Every fixture release is titled "Arrival.2016...", so identity is
		// established by title and year; none carries an id and none came
		// from an id query.
		Identity: decision.Identity{Titles: []string{"Arrival"}, Year: 2016, IDs: map[string]string{common.IDKeyTMDB: "329865"}},
	}
	o := decision.Options{
		UserInvoked:       true,
		ProtocolsEnabled:  map[string]bool{"torrent": true, "usenet": true},
		IndexerPriority:   map[string]int{"hd-tracker": 5},
		PreferredProtocol: "torrent",
	}

	ds := decision.Evaluate(context.Background(), tg, p, &catalogue.Catalogue{}, loadReleases(t), o)
	require.Len(t, ds, 4)

	require.True(t, ds[0].Approved, "Bluray-1080p, well within size bounds")
	require.Empty(t, ds[0].Rejections)
	require.Equal(t, 0, ds[0].Rank.QualityIndex)

	require.True(t, ds[1].Approved, "WEBDL-720p, lower tier but still in the profile")
	require.Equal(t, 1, ds[1].Rank.QualityIndex)

	require.False(t, ds[2].Approved, "sample release")
	require.False(t, ds[2].TemporarilyRejected, "every reason this package emits is Permanent")
	found := false
	for _, r := range ds[2].Rejections {
		if r.Reason[:len(decision.ReasonSample.Code)] == decision.ReasonSample.Code {
			found = true
		}
	}
	require.True(t, found, "expected a Sample rejection among %+v", ds[2].Rejections)

	require.False(t, ds[3].Approved, "WEBRip-1080p, 400 MB is under the 1.44 GB floor at 110 minutes")

	approved := []decision.Decision{ds[0], ds[1]}
	ranked := decision.Rank(approved, o)
	require.Equal(t, "hd-tracker:12345", ranked[0].Release.GUID, "Bluray-1080p outranks WEBDL-720p")
	require.Equal(t, "hd-tracker:12346", ranked[1].Release.GUID)
}

// TestEvaluateScoresReleaseTitleFormatsFromTheReleaseName is the X7a-found
// defect at the search end: Evaluate must hand the catalogue the indexer's
// full release name, because the parsed title is only "Heat" and every
// ReleaseTitle custom format (repack, HDR, codecs, streaming) reads the
// name. Scored against the real embedded catalogue.
func TestEvaluateScoresReleaseTitleFormatsFromTheReleaseName(t *testing.T) {
	bluray2160, ok := quality.Lookup("video", "Bluray-2160p")
	require.True(t, ok)
	p := quality.Profile{
		Tiers:        [][]quality.Definition{{bluray2160}},
		LanguageName: "any",
		Sizes:        quality.MovieSizeTable(),
		ProperPolicy: "preferAndUpgrade",
		Scores:       map[string]int{"repack-proper": 5, "hdr": 500},
	}
	tg := decision.Target{
		Kind: common.MediaKindMovie, Available: true, OriginalLanguageTag: "en",
		Identity: decision.Identity{Titles: []string{"Heat"}, Year: 1995},
	}
	rel := common.ReleaseInfo{
		GUID: "idx:heat", IndexerRef: "idx", Protocol: common.ProtocolTorrent,
		Title: "Heat.1995.REPACK.2160p.UHD.BluRay.HDR.x265-GROUP",
	}
	o := decision.Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true}}

	ds := decision.Evaluate(context.Background(), tg, p, catalogue.LoadedCatalogue(), []common.ReleaseInfo{rel}, o)
	require.Len(t, ds, 1)
	require.Contains(t, ds[0].Matched, "repack-proper")
	require.Contains(t, ds[0].Matched, "hdr")
	require.Equal(t, 505, ds[0].Score)
	require.EqualValues(t, 505, ds[0].Release.FormatScore, "the score lands on the Release that Search.status and Download.spec carry")
}
