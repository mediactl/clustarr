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
	"fmt"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The rank score's bonuses (owner, 2026-11-03: every ranking rule adds to
// the score, which decides the order, rather than bumping a release ahead
// of the score). Each bonus is larger than everything below it can add, so
// ordering by the score is the order the rules made as a chain of
// tie-breaks: complete audio under Audio.CompleteFirst, then quality tier,
// revision, a whole season, the preferred protocol and the format score.
// The format score is clamped to FormatLimit for the sum; a release
// beyond it has formats scored -10000 and is rejected.
const (
	FormatLimit      = 9_999
	ProtocolBonus    = 20_000
	WholeSeasonBonus = 40_000
	VersionBonus     = 100_000       // per version past the first, up to RevisionSteps
	RealBonus        = 1_000_000     // per REAL, up to RevisionSteps
	TierBonus        = 10_000_000    // per tier above the profile's worst, up to MaxTiers
	CompleteBonus    = 1_000_000_000 // every wanted audio language, under Audio.CompleteFirst

	RevisionSteps = 9
	MaxTiers      = 50
)

// Inputs are one release's facts the rank score adds up.
type Inputs struct {
	FormatScore int
	// PreferredProtocol: the release's protocol is the profile's preferred
	// one (any is, under "any" or none).
	PreferredProtocol bool
	// WholeSeason: a season pack, or a batch covering the whole season --
	// on a Comic, a pack covering the whole volume (2026-11-10 design D10:
	// a whole volume ranks as a whole season does).
	WholeSeason bool
	// Revision counts only when PreferRevision (ProperPolicy is not
	// doNotPrefer).
	Revision       common.Revision
	PreferRevision bool
	// Tier is the release quality's tier in the profile, 0 the best, of
	// Tiers; a quality the profile does not hold is Tier >= Tiers. Tiers 0
	// (a key built by hand) counts as MaxTiers.
	Tier, Tiers int
	// Complete: the release carries every language the audio policy wants,
	// which counts only under CompleteFirst.
	Complete, CompleteFirst bool
}

// Score is the release's rank score (Inputs): the format score
// plus each rule's bonus.
func Score(in Inputs) int64 {
	s := int64(clamp(in.FormatScore, -FormatLimit, FormatLimit))
	if in.PreferredProtocol {
		s += ProtocolBonus
	}
	if in.WholeSeason {
		s += WholeSeasonBonus
	}
	if in.PreferRevision {
		s += int64(clamp(int(in.Revision.EffectiveVersion())-1, 0, RevisionSteps)) * VersionBonus
		s += int64(clamp(int(in.Revision.Real), 0, RevisionSteps)) * RealBonus
	}
	tiers := min(in.Tiers, MaxTiers)
	if tiers <= 0 {
		tiers = MaxTiers
	}
	if in.Tier < tiers {
		s += int64(tiers-1-in.Tier) * TierBonus
	}
	if in.CompleteFirst && in.Complete {
		s += CompleteBonus
	}
	return s
}

// Parts names what a rank score of a release with formatScore is made
// of, best rule first ("complete audio +1,000,000,000", "format score
// +300"), for a person reading it.
func Parts(score int64, formatScore int) []string {
	f := int64(clamp(formatScore, -FormatLimit, FormatLimit))
	rest := score - f
	var out []string
	take := func(name string, unit int64, max int64) {
		n := rest / unit
		if max > 0 {
			n = min(n, max)
		}
		if n > 0 {
			rest -= n * unit
			if n == 1 {
				out = append(out, fmt.Sprintf("%s +%s", name, group(unit)))
			} else {
				out = append(out, fmt.Sprintf("%s +%s (%d x %s)", name, group(n*unit), n, group(unit)))
			}
		}
	}
	take("complete audio", CompleteBonus, 1)
	take("quality tier", TierBonus, MaxTiers)
	take("REAL", RealBonus, RevisionSteps)
	take("version", VersionBonus, RevisionSteps)
	take("whole season", WholeSeasonBonus, 1)
	take("preferred protocol", ProtocolBonus, 1)
	return append(out, fmt.Sprintf("format score %+d", formatScore))
}

func clamp(v, lo, hi int) int { return max(lo, min(hi, v)) }

// group writes n with thousands separators.
func group(n int64) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
