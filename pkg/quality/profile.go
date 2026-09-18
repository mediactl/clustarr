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

package quality

import (
	"context"

	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// Profile is a resolved, ready-to-evaluate QualityProfile: the CRD's Tiers
// resolved against a Catalogue into Definitions, and every custom-format
// score flattened into one map.
type Profile struct {
	Tiers                 [][]Definition // best first; members of one tier compare equal
	CutoffIndex           int            // index into Tiers
	UpgradeAllowed        bool
	MinFormatScore        int
	CutoffFormatScore     int
	MinUpgradeFormatScore int
	Scores                map[string]int // catalogue format slug -> resolved score for this profile
	Language              string
	ProperPolicy          string
	Sizes                 map[string]SizeLimit // quality name -> resolved size limits
	Hash                  string
}

// Score is catalogue.Catalogue.Score against p's resolved Scores map -- see
// that method's doc for the exact semantics. This is the spec §7 shape
// (`Catalogue.Score(p *Profile, ...)`); the free function on Catalogue
// itself cannot take *Profile without an import cycle (quality imports
// catalogue), so it takes the score map and this wrapper adapts.
func (p Profile) Score(ctx context.Context, cat *catalogue.Catalogue, r *release.ParsedRelease, ic catalogue.ItemContext) (score int, matched []string) {
	return cat.Score(ctx, p.Scores, r, ic)
}
