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

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// TestAReleaseCarriesTheLanguagesItsTitleNames: the languages a Search
// stores and the ui lists are the parser's, so a dual-audio release reads
// as the original and English under every profile, an anime one or not, and
// a bare DUBBED one as English (2026-10-19: AnoZu's Dual-Audio release of
// Let This Grieving Soul Retire! read Japanese alone). Under the dual-audio
// policy the first is complete and the dub lacks the anchor.
func TestAReleaseCarriesTheLanguagesItsTitleNames(t *testing.T) {
	const (
		dual = "Monster.S01E02.REPACK.1080p.CR.WEB-DL.H.264.Dual-Audio.AAC.2.0-AnoZu"
		dub  = "Monster.S01E02.DUBBED.1080p.WEB-DL.H.264-GRP"
	)
	plain := donorProfile(t)
	plain.ScoreSet, plain.AudioLanguages, plain.GraftAudio = "default", nil, false
	for _, p := range []quality.Profile{donorProfile(t), plain} {
		var rels []common.ReleaseInfo
		for i, title := range []string{dual, dub} {
			rels = append(rels, common.ReleaseInfo{
				GUID: "g" + string(rune('a'+i)), Title: title, IndexerRef: "nzbgeek", Protocol: common.ProtocolUsenet,
				SizeBytes: 400 << 20,
			})
		}
		ds := decision.Evaluate(context.Background(), monsterS01E02(nil), p, &catalogue.Catalogue{}, rels,
			decision.Options{ProtocolsEnabled: map[string]bool{"usenet": true}})
		require.Equal(t, []string{"Japanese", "English"}, ds[0].Release.Languages, "score set %s", p.ScoreSet)
		require.Equal(t, []string{"English"}, ds[1].Release.Languages, "score set %s", p.ScoreSet)
	}

	ds := evaluateTitles(t, monsterS01E02(nil), dual, dub)
	require.True(t, ds[0].Rank.LanguagesComplete, "%+v", ds[0].Rejections)
	require.True(t, rejectedFor(ds[1], decision.ReasonWantedLanguage), "%+v", ds[1].Rejections)
}
