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

import "github.com/mediactl/clustarr/pkg/rankscore"

// Score is the decision's rank score (rankscore.Score), which Rank orders
// by.
func (k RankKey) Score() int64 {
	return rankscore.Score(rankscore.Inputs{
		FormatScore: k.FormatScore, PreferredProtocol: k.PreferredProtocolMatch, WholeSeason: k.WholeSeason(),
		Revision: k.Revision, PreferRevision: k.PreferRevision, Tier: k.QualityIndex, Tiers: k.Tiers,
		Complete: k.LanguagesComplete, CompleteFirst: k.CompleteFirst,
	})
}
