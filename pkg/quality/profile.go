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
	// Language is QualityProfileSpec.Language verbatim: "original", "any"
	// or a BCP-47 tag. It is what Hash identifies; LanguageName is what a
	// decision engine compares against.
	Language string
	// LanguageName is Language resolved into the vocabulary the rest of
	// the pipeline speaks: the sentinels "original" and "any" pass through
	// unchanged, an empty Language stays empty (no constraint), and any
	// other value is a BCP-47 tag resolved through catalogue.LanguageName
	// to a Radarr English name ("en" -> "English"), which is what
	// release.ParsedRelease.Languages and every CondLanguage condition
	// hold. FromCRD reports an unresolvable tag as an error and leaves
	// this empty.
	LanguageName string
	ProperPolicy string
	Sizes        map[string]SizeLimit // quality name -> resolved size limits
	// PreferredProtocol ranks one transfer protocol above the other
	// (catalogv1alpha1.PreferredProtocol's string value: "usenet",
	// "torrent" or "any"). Not part of spec's one-line Profile summary,
	// added because it is part of what FromCRD resolves from
	// QualityProfileSpec and Hash must identify (spec §4.2) -- a profile
	// differing only in preferred protocol is a different resolved profile.
	// This package does not evaluate it; a later phase's release ranking
	// does.
	PreferredProtocol string
	// MediaKind is QualityProfileSpec.MediaKind ("video", "music", "book",
	// "audiobook" or "comic"). Empty reads as video, so a Profile built by
	// hand keeps the behaviour it always had. Custom formats are TRaSH's
	// Radarr/Sonarr data: only a video profile scores them (see FromCRD and
	// Score).
	MediaKind string
	Hash      string
}

// scoresFormats reports whether p is a profile custom formats apply to.
func (p Profile) scoresFormats() bool {
	return isVideoKind(p.MediaKind)
}

// isVideoKind reports whether a profile media kind is video; empty reads as
// video, as it always has.
func isVideoKind(kind string) bool {
	return kind == "" || kind == string(catalogv1alpha1.ProfileMediaKindVideo)
}

// Score is catalogue.Catalogue.Score against p's resolved Scores map -- see
// that method's doc for the exact semantics. This is the spec §7 shape
// (`Catalogue.Score(p *Profile, ...)`); the free function on Catalogue
// itself cannot take *Profile without an import cycle (quality imports
// catalogue), so it takes the score map and this wrapper adapts.
//
// A non-video profile scores nothing and matches nothing: every catalogue
// format is TRaSH video data (release-group tiers, HDR, streaming services,
// language-not-original, ...), and Catalogue.Match evaluates every format
// regardless of the score map, so without this a music release would still
// report -- and a MediaFile freeze -- video formats it "matched".
func (p Profile) Score(ctx context.Context, cat *catalogue.Catalogue, r *release.ParsedRelease, ic catalogue.ItemContext) (score int, matched []string) {
	if !p.scoresFormats() {
		return 0, nil
	}
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
			if d.holds(q) {
				return i, true
			}
		}
	}
	return 0, false
}

// holds compares q with d the way a Profile's tiers do: by name for a
// non-video quality (Source/Resolution/Modifier are always zero for
// music/book/audiobook/comic), where a release's upstream name -- Lidarr's
// "ALAC", say -- is held by the Definition that lists it as an alias; by
// (Source, Resolution, Modifier) for video (Name is display metadata there,
// not identity -- two Definitions in one default-tie Group like "WEB 1080p"
// have different Names but the same triple).
func (d Definition) holds(q common.Quality) bool {
	if isNonVideo(d.Quality) && isNonVideo(q) {
		return d.named(q.Name) || (q.Name != "" && d.Quality.Name == q.Name)
	}
	return d.Quality.Source == q.Source && d.Quality.Resolution == q.Resolution && d.Quality.Modifier == q.Modifier
}

// isNonVideo reports whether q is a name-only (non-video) quality.
func isNonVideo(q common.Quality) bool {
	return q.Source == "" && q.Resolution == 0
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
//
// Only a video profile gets custom formats. The catalogue is TRaSH's
// Radarr/Sonarr corpus, so scoring it against a music, book, audiobook or
// comic profile applied video release-group, HDR and language formats to
// releases they were never written for. A non-video profile's Scores is
// empty, and a spec that asks for formats anyway -- formatScores,
// enabledFormatGroups, or a minFormatScore above zero, which no release of
// that kind could ever reach -- is reported, as an unknown slug is, rather
// than silently doing nothing.
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
	formats := cat.Formats
	if !isVideoKind(kind) {
		formats = nil
		overrides = map[string]int32{}
		errs = append(errs, nonVideoFormatErrors(p)...)
	}
	scores := make(map[string]int, len(formats))
	for slug, f := range formats {
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

	langName, err := normaliseLanguage(p.Spec.Language)
	if err != nil {
		errs = append(errs, err)
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
		Scores:                scores, Language: p.Spec.Language, LanguageName: langName,
		ProperPolicy: string(p.Spec.ProperPolicy),
		Sizes:        sizes, PreferredProtocol: string(p.Spec.PreferredProtocol),
		MediaKind: kind,
	}
	prof.Hash = hashProfile(prof)
	return prof, errs
}

// nonVideoFormatErrors reports every custom-format setting on a non-video
// profile: each one is ignored, and a setting that silently does nothing is
// the same class of mistake as an unknown slug.
func nonVideoFormatErrors(p *catalogv1alpha1.QualityProfile) []error {
	var errs []error
	kind := p.Spec.MediaKind
	for _, fs := range p.Spec.FormatScores {
		errs = append(errs, fmt.Errorf("formatScores: custom formats apply to video profiles only; a %s profile cannot score %q", kind, fs.Format))
	}
	if len(p.Spec.EnabledFormatGroups) > 0 {
		errs = append(errs, fmt.Errorf("enabledFormatGroups: custom formats apply to video profiles only; a %s profile has none to enable (%s)",
			kind, strings.Join(p.Spec.EnabledFormatGroups, ", ")))
	}
	if p.Spec.MinFormatScore > 0 {
		errs = append(errs, fmt.Errorf("minFormatScore %d can never be met: a %s profile scores no custom formats, so every release scores 0",
			p.Spec.MinFormatScore, kind))
	}
	return errs
}

// normaliseLanguage resolves QualityProfileSpec.Language ("original",
// "any" or a BCP-47 tag) into the vocabulary release.ParsedRelease.Languages
// and the catalogue's CondLanguage conditions use (Radarr's English names).
// The two sentinels and the empty value pass through unchanged; anything
// else must resolve, or it is an error the caller surfaces on the profile's
// Invalid condition rather than silently accepting a language that can
// never match.
func normaliseLanguage(lang string) (string, error) {
	switch {
	case lang == "":
		return "", nil
	case strings.EqualFold(lang, "original"), strings.EqualFold(lang, "any"):
		return lang, nil
	}
	name, ok := catalogue.LanguageName(lang)
	if !ok {
		return "", fmt.Errorf(
			"language %q is not supported: expected \"original\", \"any\", or the BCP-47 tag of a language Radarr knows (its primary subtag must be an ISO-639-1 code in pkg/quality/catalogue's language table, e.g. \"en\", \"es\", \"ja\", \"pt-BR\")",
			lang)
	}
	return name, nil
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
	// hash.Hash.Write (via fmt.Fprintf) never returns an error per its own
	// doc contract, so every Fprintf return here is deliberately ignored.
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "cutoff=%d|upgrade=%t|min=%d|cutoffFmt=%d|minUpgrade=%d|lang=%s|proper=%s|protocol=%s\n",
		p.CutoffIndex, p.UpgradeAllowed, p.MinFormatScore, p.CutoffFormatScore, p.MinUpgradeFormatScore, p.Language, p.ProperPolicy, p.PreferredProtocol)
	for _, tier := range p.Tiers {
		names := make([]string, len(tier))
		for i, d := range tier {
			names[i] = d.Name
		}
		_, _ = fmt.Fprintf(h, "tier=%s\n", strings.Join(names, ","))
	}
	keys := make([]string, 0, len(p.Scores))
	for k := range p.Scores {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		_, _ = fmt.Fprintf(h, "score=%s:%d\n", k, p.Scores[k])
	}
	sizeKeys := make([]string, 0, len(p.Sizes))
	for k := range p.Sizes {
		sizeKeys = append(sizeKeys, k)
	}
	sort.Strings(sizeKeys)
	for _, k := range sizeKeys {
		lim := p.Sizes[k]
		_, _ = fmt.Fprintf(h, "size=%s:%g,%g,%g\n", k, lim.MinMBPerMin, lim.PrefMBPerMin, lim.MaxMBPerMin)
	}
	return hex.EncodeToString(h.Sum(nil))
}
