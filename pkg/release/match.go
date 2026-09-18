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

package release

import "github.com/lithammer/fuzzysearch/fuzzy"

// MatchTitle scores p against cands with fuzzysearch.RankMatch on
// CleanTitle(p.Title) vs. CleanTitle(cand.Title), penalized by |year delta|;
// best is the index of the top-scoring candidate, or -1 when cands is empty.
// score follows fuzzysearch's own float-scored ranking (not a status or
// telemetry field — no scaled-int substitute applies here).
//
// fuzzy.RankMatchNormalized returns a Levenshtein-style edit distance where
// *lower* is a better match and -1 means "no match at all" (a required
// source rune is entirely absent from the target, or the target is shorter
// than the source) — the opposite convention from "higher is better". This
// inverts that distance into a similarity score (source rune count minus
// the distance, so a perfect match is a clearly positive number and a poor
// one trends negative) rather than negating it directly, because a bare
// negation would put a perfect match's score at 0, not distinguishably
// better than an empty-candidates call's zero value.
func MatchTitle(p *ParsedRelease, cands []TitleCandidate) (best int, score float64) {
	best = -1
	clean := CleanTitle(p.Title)
	cleanLen := float64(len([]rune(clean)))

	for i, c := range cands {
		rank := fuzzy.RankMatchNormalized(clean, CleanTitle(c.Title))
		if rank < 0 {
			continue // no match at all: never a candidate, regardless of year
		}
		s := cleanLen - float64(rank)
		if c.Year != 0 && p.Year != 0 && c.Year != p.Year {
			s -= 1000 // year mismatch is disqualifying but not a hard reject —
			// a wrong-year near-exact title still beats no match at all
		}
		if best == -1 || s > score {
			best, score = i, s
		}
	}
	return best, score
}
