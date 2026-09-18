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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
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

// FromCRD resolves p against cat: looks up every Tiers[].Qualities[] entry
// with Lookup, resolves the cutoff tier index, and flattens cat.Formats into
// Profile.Scores honoring p.Spec.EnabledFormatGroups (a format whose Group is
// non-empty and not enabled contributes nothing) and p.Spec.FormatScores
// overrides. Every problem (unknown quality name, cutoff naming no tier,
// FormatScores referencing an unknown slug) is collected into the returned
// slice rather than failing fast, so a caller can report every issue at once
// via the Invalid condition (spec §4.2).
func FromCRD(p *catalogv1alpha1.QualityProfile, cat *catalogue.Catalogue) (Profile, []error) {
	var errs []error
	kind := string(p.Spec.MediaKind)

	tiers := make([][]Definition, 0, len(p.Spec.Tiers))
	tierIndexByName := map[string]int{}
	for _, t := range p.Spec.Tiers {
		var defs []Definition
		for _, qname := range t.Qualities {
			d, ok := Lookup(kind, qname)
			if !ok {
				errs = append(errs, fmt.Errorf("tier %q: unknown quality %q", t.Name, qname))
				continue
			}
			defs = append(defs, d)
		}
		tierIndexByName[t.Name] = len(tiers)
		tiers = append(tiers, defs)
	}
	cutoffIdx, ok := tierIndexByName[p.Spec.Cutoff]
	if !ok {
		errs = append(errs, fmt.Errorf("cutoff %q does not name a tier", p.Spec.Cutoff))
	}

	upgradeAllowed := true
	if p.Spec.UpgradeAllowed != nil {
		upgradeAllowed = *p.Spec.UpgradeAllowed
	}
	scoreSet := string(p.Spec.ScoreSet)
	if scoreSet == "" {
		scoreSet = "default"
	}
	enabled := make(map[string]bool, len(p.Spec.EnabledFormatGroups))
	for _, g := range p.Spec.EnabledFormatGroups {
		enabled[g] = true
	}
	overrides := make(map[string]int32, len(p.Spec.FormatScores))
	for _, fs := range p.Spec.FormatScores {
		overrides[fs.Format] = fs.Score
	}
	scores := make(map[string]int, len(cat.Formats))
	for slug, f := range cat.Formats {
		if f.Group != "" && !enabled[f.Group] {
			continue
		}
		if ov, has := overrides[slug]; has {
			scores[slug] = int(ov)
			delete(overrides, slug)
			continue
		}
		if s, has := f.Scores[scoreSet]; has {
			scores[slug] = s
		} else if s, has := f.Scores["default"]; has {
			scores[slug] = s
		}
	}
	// Whatever is left in overrides references a slug FromCRD never saw in
	// cat.Formats (Group-gated slugs already consumed their override above,
	// so this is genuinely unknown, not merely disabled).
	unknownSlugs := make([]string, 0, len(overrides))
	for slug := range overrides {
		unknownSlugs = append(unknownSlugs, slug)
	}
	sort.Strings(unknownSlugs)
	for _, slug := range unknownSlugs {
		errs = append(errs, fmt.Errorf("formatScores references unknown format %q", slug))
	}

	sizes := baseSizeTable(string(p.Spec.SizeTable))
	for _, sl := range p.Spec.SizeLimits {
		lim := sizes[sl.Quality]
		if sl.MinMBPerMinute != nil {
			lim.MinMBPerMin = sl.MinMBPerMinute.AsApproximateFloat64()
		}
		if sl.PreferredMBPerMinute != nil {
			lim.PrefMBPerMin = sl.PreferredMBPerMinute.AsApproximateFloat64()
		}
		if sl.MaxMBPerMinute != nil {
			lim.MaxMBPerMin = sl.MaxMBPerMinute.AsApproximateFloat64()
		}
		sizes[sl.Quality] = lim
	}

	prof := Profile{
		Tiers: tiers, CutoffIndex: cutoffIdx, UpgradeAllowed: upgradeAllowed,
		MinFormatScore: int(p.Spec.MinFormatScore), CutoffFormatScore: int(p.Spec.CutoffFormatScore),
		MinUpgradeFormatScore: int(p.Spec.MinUpgradeFormatScore),
		Scores:                scores, Language: p.Spec.Language, ProperPolicy: string(p.Spec.ProperPolicy),
		Sizes: sizes,
	}
	prof.Hash = hashProfile(prof)
	return prof, errs
}

// baseSizeTable resolves a QualityProfileSpec.SizeTable selection to its
// underlying table. "none" (and any unrecognized value) resolves to an empty
// table, matching SizeTableNone's "no size checking" semantics.
func baseSizeTable(name string) map[string]SizeLimit {
	switch name {
	case "movie":
		return MovieSizeTable()
	case "series":
		return SeriesSizeTable()
	case "anime":
		return AnimeSizeTable()
	default:
		return map[string]SizeLimit{}
	}
}

// hashProfile is a deterministic digest of everything that changes a
// Profile's evaluation outcome, so MediaFile can record it at import time
// (spec §4.2: "Hash identifies the resolved profile") and a later reconcile
// can detect drift. Map iteration order in Go is randomized, so every map is
// sorted before hashing.
func hashProfile(p Profile) string {
	h := sha256.New()
	fmt.Fprintf(h, "cutoff=%d|upgrade=%t|min=%d|cutoffFmt=%d|minUpgrade=%d|lang=%s|proper=%s\n",
		p.CutoffIndex, p.UpgradeAllowed, p.MinFormatScore, p.CutoffFormatScore, p.MinUpgradeFormatScore, p.Language, p.ProperPolicy)
	for _, tier := range p.Tiers {
		names := make([]string, len(tier))
		for i, d := range tier {
			names[i] = d.Name
		}
		fmt.Fprintf(h, "tier=%s\n", strings.Join(names, ","))
	}
	keys := make([]string, 0, len(p.Scores))
	for k := range p.Scores {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "score=%s:%d\n", k, p.Scores[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}
