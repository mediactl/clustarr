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

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
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

// Index returns q's tier position (0 = best) and whether q is allowed at
// all. Two Definitions in the same tier compare equal for upgrade purposes
// (docs/research/quality.md §3.2's QualityModelComparer, "group members
// tie"); this matches by (Source, Resolution, Modifier) for video and by
// Name otherwise, never by weight.
func (p Profile) Index(q common.Quality) (idx int, ok bool) {
	for i, tier := range p.Tiers {
		for _, d := range tier {
			if sameQuality(d.Quality, q) {
				return i, true
			}
		}
	}
	return 0, false
}

// sameQuality compares two Quality values the way a Profile's tiers do: by
// name for a non-video quality (Source/Resolution/Modifier are always zero
// for music/book/audiobook/comic), by (Source, Resolution, Modifier) for
// video (Name is display metadata there, not identity -- two Definitions in
// one default-tie Group like "WEB 1080p" have different Names but the same
// triple).
func sameQuality(a, b common.Quality) bool {
	if a.Source == "" && b.Source == "" && a.Resolution == 0 && b.Resolution == 0 {
		return a.Name != "" && a.Name == b.Name
	}
	return a.Source == b.Source && a.Resolution == b.Resolution && a.Modifier == b.Modifier
}

// Allowed reports whether q appears in any tier of p.
func (p Profile) Allowed(q common.Quality) bool {
	_, ok := p.Index(q)
	return ok
}

// CutoffMet reports whether q's tier is at or better than p's cutoff tier.
// Lower Index is better (best-first ordering), so "met" means idx <=
// CutoffIndex. An unallowed q never meets cutoff.
func (p Profile) CutoffMet(q common.Quality) bool {
	idx, ok := p.Index(q)
	return ok && idx <= p.CutoffIndex
}
