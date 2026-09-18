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
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

func TestProtocolRejection(t *testing.T) {
	t.Run("enabled protocol passes", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{"torrent": true}}
		require.Nil(t, protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolTorrent}, o))
	})
	t.Run("disabled protocol rejects", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{"torrent": false, "usenet": true}}
		got := protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolTorrent}, o)
		require.NotNil(t, got)
		require.Equal(t, common.RejectionPermanent, got.Type)
	})
	t.Run("a protocol missing from the map is disabled (fail closed)", func(t *testing.T) {
		o := Options{ProtocolsEnabled: map[string]bool{}}
		require.NotNil(t, protocolRejection(common.ReleaseInfo{Protocol: common.ProtocolUsenet}, o))
	})
}

func TestAvailabilityRejection(t *testing.T) {
	t.Run("unavailable and not user-invoked rejects", func(t *testing.T) {
		got := availabilityRejection(Target{Available: false}, Options{UserInvoked: false})
		require.NotNil(t, got)
	})
	t.Run("unavailable but user-invoked is skipped -- naming.md §A6: interactive search skips monitored-type checks", func(t *testing.T) {
		require.Nil(t, availabilityRejection(Target{Available: false}, Options{UserInvoked: true}))
	})
	t.Run("available passes regardless", func(t *testing.T) {
		require.Nil(t, availabilityRejection(Target{Available: true}, Options{UserInvoked: false}))
	})
}

func TestQualityRejections(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{Tiers: [][]quality.Definition{{bluray1080}}, MinFormatScore: 10}

	t.Run("allowed quality, score above minimum", func(t *testing.T) {
		require.Empty(t, qualityRejections(p, common.ReleaseInfo{Quality: bluray1080.Quality}, 10))
	})
	t.Run("quality not in any tier", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: webdl720.Quality}, 10)
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonQualityNotWanted.Code)
	})
	t.Run("score below MinFormatScore", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: bluray1080.Quality}, 9)
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonCustomFormatMinimumScore.Code)
	})
	t.Run("both fail at once", func(t *testing.T) {
		got := qualityRejections(p, common.ReleaseInfo{Quality: webdl720.Quality}, 0)
		require.Len(t, got, 2)
	})
}

func TestLanguageRejection(t *testing.T) {
	t.Run("any accepts everything", func(t *testing.T) {
		p := quality.Profile{LanguageName: "any"}
		require.Nil(t, languageRejection(Target{}, p, &release.ParsedRelease{Languages: []string{"French"}}))
	})
	t.Run("original language present", func(t *testing.T) {
		p := quality.Profile{LanguageName: "original"}
		tg := Target{OriginalLanguage: "Japanese"}
		require.Nil(t, languageRejection(tg, p, &release.ParsedRelease{Languages: []string{"Japanese", "English"}}))
	})
	t.Run("original language missing rejects", func(t *testing.T) {
		p := quality.Profile{LanguageName: "original"}
		tg := Target{OriginalLanguage: "Japanese"}
		got := languageRejection(tg, p, &release.ParsedRelease{Languages: []string{"English"}})
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonWantedLanguage.Code)
	})
	t.Run("specific wanted language missing rejects", func(t *testing.T) {
		p := quality.Profile{LanguageName: "German"}
		require.NotNil(t, languageRejection(Target{}, p, &release.ParsedRelease{Languages: []string{"English"}}))
	})
	t.Run("empty LanguageName means no constraint", func(t *testing.T) {
		p := quality.Profile{LanguageName: ""}
		require.Nil(t, languageRejection(Target{}, p, &release.ParsedRelease{Languages: nil}))
	})
}

func TestSampleRejection(t *testing.T) {
	t.Run("small file with sample in the title rejects", func(t *testing.T) {
		got := sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.1080p.BluRay.x264-GROUP.sample", SizeBytes: 41_943_040}) // 40 MiB
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonSample.Code)
	})
	t.Run("sample in the title but large enough to be a real file", func(t *testing.T) {
		require.Nil(t, sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.sample.pack.1080p.BluRay-GROUP", SizeBytes: 10_000_000_000}))
	})
	t.Run("no sample token", func(t *testing.T) {
		require.Nil(t, sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.1080p.BluRay.x264-GROUP", SizeBytes: 1000}))
	})
	t.Run("case-insensitive", func(t *testing.T) {
		require.NotNil(t, sampleRejection(common.ReleaseInfo{Title: "Arrival.2016.SAMPLE.mkv", SizeBytes: 1}))
	})
}

func TestBlocklistAndAlreadyImportedRejections(t *testing.T) {
	t.Run("blocklisted", func(t *testing.T) {
		tg := Target{Blocklist: func(hash, title string) bool { return hash == "deadbeef" }}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{InfoHash: "deadbeef", Title: "x"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonBlocklisted.Code)
	})
	t.Run("nil Blocklist func never rejects", func(t *testing.T) {
		require.Empty(t, blocklistAndHistoryRejections(Target{}, common.ReleaseInfo{InfoHash: "x"}))
	})
	t.Run("same hash as the currently-imported file's source", func(t *testing.T) {
		tg := Target{Current: &Current{SourceHash: "ABCDEF", SourceTitle: "Some.Other.Title"}}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{InfoHash: "abcdef", Title: "Different.Title"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonAlreadyImportedSameHash.Code)
	})
	t.Run("same title as the currently-imported file's source (usenet, no hash)", func(t *testing.T) {
		tg := Target{Current: &Current{SourceTitle: "Arrival.2016.1080p.BluRay.x264-GROUP"}}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{Title: "arrival.2016.1080p.bluray.x264-group"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonAlreadyImportedSameName.Code)
	})
	t.Run("no Current means never already-imported", func(t *testing.T) {
		require.Empty(t, blocklistAndHistoryRejections(Target{}, common.ReleaseInfo{Title: "anything"}))
	})
	t.Run("hash takes priority over a simultaneous name match, one rejection only", func(t *testing.T) {
		tg := Target{Current: &Current{SourceHash: "AAAA", SourceTitle: "Same.Title"}}
		got := blocklistAndHistoryRejections(tg, common.ReleaseInfo{InfoHash: "aaaa", Title: "Same.Title"})
		require.Len(t, got, 1)
		require.Contains(t, got[0].Reason, ReasonAlreadyImportedSameHash.Code)
	})
}

func TestQueueRejection(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{
		Tiers: [][]quality.Definition{{bluray1080}, {webdl720}}, CutoffIndex: 0,
		UpgradeAllowed: true, CutoffFormatScore: 10000, MinUpgradeFormatScore: 1, ProperPolicy: "preferAndUpgrade",
	}

	t.Run("nothing queued", func(t *testing.T) {
		require.Nil(t, queueRejection(p, Target{}, quality.Candidate{Quality: bluray1080.Quality}))
	})
	t.Run("queued release is equal-or-better, candidate rejected", func(t *testing.T) {
		tg := Target{Queue: []Queued{{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}}}
		candidate := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		got := queueRejection(p, tg, candidate)
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonQueueHigherPreference.Code)
	})
	t.Run("candidate is strictly better than what's queued, not rejected", func(t *testing.T) {
		tg := Target{Queue: []Queued{{Quality: webdl720.Quality}}}
		candidate := quality.Candidate{Quality: bluray1080.Quality}
		require.Nil(t, queueRejection(p, tg, candidate))
	})
}

func TestUpgradeRejection(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl720, _ := quality.Lookup("video", "WEBDL-720p")
	p := quality.Profile{
		Tiers: [][]quality.Definition{{bluray1080}, {webdl720}}, CutoffIndex: 0,
		UpgradeAllowed: true, CutoffFormatScore: 10000, MinUpgradeFormatScore: 1, ProperPolicy: "preferAndUpgrade",
	}

	t.Run("no current file, nothing to upgrade over", func(t *testing.T) {
		require.Nil(t, upgradeRejection(p, Target{}, quality.Candidate{Quality: webdl720.Quality}))
	})
	t.Run("candidate is a real upgrade, not rejected", func(t *testing.T) {
		tg := Target{Current: &Current{Quality: webdl720.Quality, Revision: common.Revision{Version: 1}}}
		got := upgradeRejection(p, tg, quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}})
		require.Nil(t, got)
	})
	t.Run("candidate is worse quality than current, rejected as ExistingHigherPreference", func(t *testing.T) {
		tg := Target{Current: &Current{Quality: bluray1080.Quality}}
		got := upgradeRejection(p, tg, quality.Candidate{Quality: webdl720.Quality})
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonExistingHigherPreference.Code)
	})
	t.Run("upgrades not allowed on the profile", func(t *testing.T) {
		np := p
		np.UpgradeAllowed = false
		tg := Target{Current: &Current{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}}
		got := upgradeRejection(np, tg, quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}})
		require.NotNil(t, got)
		require.Contains(t, got.Reason, ReasonUpgradesNotAllowed.Code)
	})
}

func TestEvaluateAdversarial(t *testing.T) {
	p := quality.Profile{LanguageName: "any", Sizes: quality.MovieSizeTable()}
	tg := Target{Kind: common.MediaKindMovie, Available: true}
	o := Options{UserInvoked: true, ProtocolsEnabled: map[string]bool{"torrent": true, "usenet": true}}

	t.Run("unparseable title rejects with exactly one reason and Parsed is nil", func(t *testing.T) {
		rel := common.ReleaseInfo{Title: "", Protocol: common.ProtocolTorrent}
		ds := Evaluate(context.Background(), tg, p, &catalogue.Catalogue{}, []common.ReleaseInfo{rel}, o)
		require.Len(t, ds, 1)
		require.False(t, ds[0].Approved)
		require.Len(t, ds[0].Rejections, 1, "an unparseable title short-circuits to exactly one Rejection, not the full checklist")
		require.Nil(t, ds[0].Parsed)
	})

	t.Run("empty release slice returns an empty, non-nil slice", func(t *testing.T) {
		ds := Evaluate(context.Background(), tg, p, &catalogue.Catalogue{}, nil, o)
		require.NotNil(t, ds)
		require.Empty(t, ds)
	})

	t.Run("Rank on an empty slice does not panic", func(t *testing.T) {
		require.Empty(t, Rank(nil, o))
	})
}
