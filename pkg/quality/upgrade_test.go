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

package quality_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// Every subtest below maps to a specific branch of
// docs/research/quality.md §6.1's UpgradableSpecification.IsUpgradable
// decision table, ported verbatim by upgrade.go's UpgradeDecision.
func TestUpgradeDecisionMatchesIsUpgradableTable(t *testing.T) {
	bluray1080, _ := quality.Lookup("video", "Bluray-1080p")
	webdl1080, _ := quality.Lookup("video", "WEBDL-1080p")

	base := quality.Profile{
		Tiers:                 [][]quality.Definition{{bluray1080}, {webdl1080}},
		CutoffIndex:           0,
		UpgradeAllowed:        true,
		CutoffFormatScore:     10000,
		MinUpgradeFormatScore: 1,
		ProperPolicy:          "preferAndUpgrade",
	}

	t.Run("better quality, cutoff not met -> Upgrade", func(t *testing.T) {
		current := quality.Candidate{Quality: webdl1080.Quality, Revision: common.Revision{Version: 1}}
		candidate := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		require.Equal(t, quality.Upgrade, base.UpgradeDecision(current, candidate))
	})

	t.Run("worse quality -> ExistingBetterQuality", func(t *testing.T) {
		current := quality.Candidate{Quality: bluray1080.Quality}
		candidate := quality.Candidate{Quality: webdl1080.Quality}
		require.Equal(t, quality.ExistingBetterQuality, base.UpgradeDecision(current, candidate))
	})

	t.Run("same quality, candidate is a proper -> Upgrade", func(t *testing.T) {
		current := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		candidate := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 2}}
		require.Equal(t, quality.Upgrade, base.UpgradeDecision(current, candidate))
	})

	t.Run("upgrades not allowed -> UpgradesNotAllowed", func(t *testing.T) {
		p := base
		p.UpgradeAllowed = false
		c := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		require.Equal(t, quality.UpgradesNotAllowed, p.UpgradeDecision(c, c))
	})

	t.Run("same quality, candidate has a worse revision -> ExistingBetterRevision", func(t *testing.T) {
		current := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 2}}
		candidate := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		require.Equal(t, quality.ExistingBetterRevision, base.UpgradeDecision(current, candidate))
	})

	t.Run("higher quality but current already meets a lower cutoff -> QualityCutoffMet", func(t *testing.T) {
		p := base
		p.CutoffIndex = 1 // WEB 1080p is the cutoff, not Bluray-1080p
		current := quality.Candidate{Quality: webdl1080.Quality, Revision: common.Revision{Version: 1}}
		candidate := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		require.Equal(t, quality.QualityCutoffMet, p.UpgradeDecision(current, candidate))
	})

	t.Run("same quality and revision, candidate score not higher -> FormatScoreNotHigher", func(t *testing.T) {
		q := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		current := q
		current.FormatScore = 100
		candidate := q
		candidate.FormatScore = 100
		require.Equal(t, quality.FormatScoreNotHigher, base.UpgradeDecision(current, candidate))
	})

	t.Run("current already at the format-score cutoff -> FormatCutoffMet", func(t *testing.T) {
		q := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		current := q
		current.FormatScore = 10000
		candidate := q
		candidate.FormatScore = 10500
		require.Equal(t, quality.FormatCutoffMet, base.UpgradeDecision(current, candidate))
	})

	t.Run("candidate score higher but below the minimum increment -> FormatIncrementTooSmall", func(t *testing.T) {
		p := base
		p.MinUpgradeFormatScore = 5
		q := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		current := q
		current.FormatScore = 100
		candidate := q
		candidate.FormatScore = 102
		require.Equal(t, quality.FormatIncrementTooSmall, p.UpgradeDecision(current, candidate))
	})

	// Revision.Version has no omitempty, so its CRD default of 1 never
	// reaches a Revision written from Go: an unparsed one arrives as 0. It
	// must read as the original, or a plain original of the same quality
	// would look like a proper of it and be grabbed as an upgrade.
	t.Run("same quality, current revision unset (0) vs an original (1) -> not an upgrade", func(t *testing.T) {
		current := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{}, FormatScore: 100}
		candidate := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}, FormatScore: 100}
		require.Equal(t, quality.FormatScoreNotHigher, base.UpgradeDecision(current, candidate))
	})

	t.Run("candidate score clears the minimum increment -> Upgrade", func(t *testing.T) {
		p := base
		p.MinUpgradeFormatScore = 5
		q := quality.Candidate{Quality: bluray1080.Quality, Revision: common.Revision{Version: 1}}
		current := q
		current.FormatScore = 100
		candidate := q
		candidate.FormatScore = 110
		require.Equal(t, quality.Upgrade, p.UpgradeDecision(current, candidate))
	})
}

// TestUpgradeDecisionWhenNeitherQualityIsInAnyTierDoesNotPanic is the
// adversarial pass over a zero-value Profile (empty Tiers): a candidate
// quality that resolves to no tier must lose, not panic Index.
func TestUpgradeDecisionWhenNeitherQualityIsInAnyTierDoesNotPanic(t *testing.T) {
	var p quality.Profile
	got := p.UpgradeDecision(quality.Candidate{}, quality.Candidate{})
	require.Equal(t, quality.ExistingBetterQuality, got)
}
