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

package rankscore

import (
	"testing"

	"github.com/stretchr/testify/assert"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Each rule's bonus outweighs everything the rules below it can add, so the
// rank score orders releases as the old chain of tie-breaks did (owner,
// 2026-11-03): complete audio, tier, REAL, version, whole season, protocol,
// format score.
func TestEachRankBonusOutweighsEverythingBelowIt(t *testing.T) {
	v1 := common.Revision{Version: 1}
	best := func(in Inputs) Inputs {
		in.FormatScore, in.PreferredProtocol, in.WholeSeason = FormatLimit, true, true
		in.PreferRevision, in.Revision = true, common.Revision{Version: 1 + RevisionSteps, Real: RevisionSteps}
		in.Tier, in.Tiers = 0, MaxTiers
		return in
	}
	worst := func(in Inputs) Inputs {
		in.FormatScore, in.PreferRevision, in.Revision, in.Tier, in.Tiers = -FormatLimit, true, v1, MaxTiers-1, MaxTiers
		return in
	}
	steps := []struct {
		name         string
		above, below Inputs
	}{
		{"complete audio over the best of everything else",
			worst(Inputs{Complete: true, CompleteFirst: true}), best(Inputs{CompleteFirst: true})},
		{"a tier over the best below it",
			func() Inputs { w := worst(Inputs{}); w.Tier = MaxTiers - 2; return w }(),
			func() Inputs { b := best(Inputs{}); b.Tier = MaxTiers - 1; return b }()},
		{"REAL over every version below it",
			Inputs{FormatScore: -FormatLimit, PreferRevision: true, Revision: common.Revision{Version: 1, Real: 1}},
			Inputs{FormatScore: FormatLimit, PreferredProtocol: true, WholeSeason: true, PreferRevision: true,
				Revision: common.Revision{Version: 1 + RevisionSteps}}},
		{"a version over a whole season, protocol and format",
			Inputs{FormatScore: -FormatLimit, PreferRevision: true, Revision: common.Revision{Version: 2}},
			Inputs{FormatScore: FormatLimit, PreferredProtocol: true, WholeSeason: true, PreferRevision: true, Revision: v1}},
		{"a whole season over protocol and format",
			Inputs{FormatScore: -FormatLimit, WholeSeason: true}, Inputs{FormatScore: FormatLimit, PreferredProtocol: true}},
		{"the preferred protocol over format",
			Inputs{FormatScore: -FormatLimit, PreferredProtocol: true}, Inputs{FormatScore: FormatLimit}},
	}
	for _, s := range steps {
		assert.Greater(t, Score(s.above), Score(s.below), s.name)
	}
	assert.Equal(t, Score(Inputs{Complete: true}), Score(Inputs{}), "complete audio counts only under completeFirst")
	assert.Equal(t, Score(Inputs{Revision: common.Revision{Version: 3}}), Score(Inputs{}),
		"a profile that does not prefer revisions scores none")
}

func TestScorePartsNamesEachBonus(t *testing.T) {
	in := Inputs{FormatScore: 300, PreferredProtocol: true, WholeSeason: true, Complete: true, CompleteFirst: true,
		PreferRevision: true, Revision: common.Revision{Version: 2}, Tier: 0, Tiers: 3}
	assert.Equal(t, []string{
		"complete audio +1,000,000,000",
		"quality tier +20,000,000 (2 x 10,000,000)",
		"version +100,000",
		"whole season +40,000",
		"preferred protocol +20,000",
		"format score +300",
	}, Parts(Score(in), in.FormatScore))
	assert.Equal(t, []string{"format score -10000"}, Parts(Score(Inputs{FormatScore: -10000, Tier: 0, Tiers: 1}), -10000))
}
