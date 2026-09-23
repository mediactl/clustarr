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

import (
	"fmt"
	"strings"
	"time"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// movieYearTolerance is how far a release's parsed year may sit from the
// movie's own Year and still be the same film.
//
// Radarr does NOT use a tolerance: ParsingService.TryGetMovieBySearchCriteria
// accepts a title match only when the parsed year equals Year or
// SecondaryYear, and QueryExtensions.AllWithYear filters the same way
// (Radarr src/NzbDrone.Core/Parser/ParsingService.cs,
// src/NzbDrone.Core/Movies/QueryExtensions.cs; docs/research/ carries no note
// on this rule). Clustarr carries SecondaryYear too
// (MovieMetadata.secondaryYear, Identity.SecondaryYear) and accepts it
// exactly, as Radarr does -- but a provider fills it only for some films, and
// a release named by the year either side of a premiere is common, so ruling
// R-7 keeps one year either side of Year as well. Dune (2021) versus Dune
// (1984) is 37 years apart, so the tolerance costs nothing where it matters.
const movieYearTolerance = 1

// minPlausibleYear is Radarr's own "the title named no year" cut:
// ParsingService.cs treats a parsed year below 1800 as absent and skips the
// year comparison entirely, which is also what this package does with
// release.ParsedRelease.Year == 0.
const minPlausibleYear = 1800

// identityRejection answers the question every other check on the §8.2
// checklist takes for granted: is this release FOR the target item at all?
// It is Radarr's Search/MovieSpecification ("Wrong movie") and Sonarr's
// SeriesSpecification / SingleEpisodeSearchMatchSpecification ("Wrong
// series", "Wrong season", "Episode wasn't requested", "Full season pack"),
// folded into one check with two verdicts about the item -- ReasonWrongItem
// when the evidence names a different item, ReasonUnknownItem when there is
// no evidence either way -- and one about what was asked for,
// ReasonFullSeason.
//
// Until G1-6 an automatic search was id-only, and an indexer answers an id
// query by matching the id server-side, so the question never came up. G1-6
// added a text fallback ("<title> <year>") for indexers that support none of
// the item's ids, and a keyword search has no such guarantee: "Dune 2021"
// can return Dune (1984) and Dune: Part Two, and before this check either
// would be approved and grabbed if quality and size passed.
//
// The item half decides in this order, first answer wins:
//
//  1. Ids, when both sides carry the same key (identityIDKeys): any conflict
//     is WrongItem even if the title matches -- a release contradicting
//     itself is not proven to be anything -- and a match with no conflict
//     settles identity regardless of title AND year, because titles
//     legitimately differ by region and language (Radarr's search path does
//     the same: its tmdbId/imdbId fallback in TryGetMovieBySearchCriteria has
//     no year check).
//  2. The indexer's id query (Identity.IDQueryIndexers), for a release that
//     carries no comparable id of its own: the indexer already matched the
//     item's id server-side, which is exactly the trust every automatic
//     search ran on before G1-6. Rejecting such a release on title grounds
//     would newly refuse the region- and language-titled releases id search
//     exists to find. The YEAR still applies to a movie, though: a release
//     the indexer filed under the right id but whose own title names a year
//     decades away is a mis-tagged tracker entry, and the year is the one
//     piece of the title that does not change with language.
//  3. Titles: every cleaned release title (ParsedRelease.Titles, so an "AKA"
//     second title counts) against every cleaned title the item is known by
//     (Identity.Titles), then for a movie the year (movieYearRejection:
//     SecondaryYear exactly, or Year within movieYearTolerance). The
//     comparison key is titleKey's, not bare release.CleanTitle.
//  4. Nothing to compare -- the unevaluable case -- is UnknownItem. See below.
//
// An episode or pack target then has to pass the numbering half too
// (numberingRejection): the right series is not the right episode. Last, a
// single-episode search refuses a full-season pack (seasonPackRejection).
//
// # The unevaluable case fails closed
//
// release.CleanTitle keeps only ASCII letters and digits (a carried defect in
// pkg/release), so a wholly non-Latin title cleans to nothing. When that
// release also carries none of the item's ids and did not come from an id
// query, there is no evidence about what it is, and this check REJECTS it as
// UnknownItem. Failing open would approve exactly the releases hardest for a
// human to check by eye, found by exactly the query least able to guarantee
// them -- a keyword search -- which is the wrong-film grab this check exists
// to stop. The cost is bounded and visible: it bites only text-fallback
// results (ids and id queries still identify a non-Latin release), each one
// carries a reason naming the cause, and an interactive user can still grab
// it through Search.spec.override. The same rule covers an empty Identity,
// so a caller that never filled one approves nothing rather than everything.
// This is the opposite choice from languageRejection's unknown-language
// branch on purpose: a release of the wrong language is still the right
// item; a release of the wrong item is a wasted download and a wrong import.
//
// targetTitles is targetTitleKeys(t.Kind, t.Identity), which Evaluate
// computes once for all of a call's releases.
func identityRejection(t Target, targetTitles map[string]struct{}, parsed *release.ParsedRelease, rel common.ReleaseInfo) *common.Rejection {
	switch t.Kind {
	case common.MediaKindMovie, common.MediaKindEpisode, common.MediaKindSeries:
	default:
		// No caller evaluates another kind today (the search worker and the
		// RSS matcher both scope themselves to video). Whoever adds one must
		// add its identity rule; until then nothing of that kind is approved.
		r := newRejection(ReasonUnknownItem, "pkg/decision has no identity rule for kind %q", t.Kind)
		return &r
	}
	if r := itemRejection(t.Kind, t.Identity, targetTitles, parsed, rel); r != nil {
		return r
	}
	if t.Kind == common.MediaKindMovie {
		return nil
	}
	if r := numberingRejection(t.Identity, parsed); r != nil {
		return r
	}
	return seasonPackRejection(t.Identity, parsed)
}

// seasonPackRejection is ruling R-3: a single-episode search does not take a
// full-season pack, even one that covers the episode. Sonarr's
// SingleEpisodeSearchMatchSpecification rejects it "Full season pack" (for a
// standard series whenever the release names no episode; for anime whenever
// it is a FullSeason and the search is not a season search). The numbering
// half has already passed by the time this runs, so the pack is of the right
// season -- a pack of another season is WrongItem, not this.
func seasonPackRejection(id Identity, p *release.ParsedRelease) *common.Rejection {
	if !id.SingleEpisodeSearch || !p.FullSeason {
		return nil
	}
	r := newRejection(ReasonFullSeason, "release is %s, and this is a search for the single episode %s",
		describeRelease(p), describeTarget(id))
	return &r
}

// itemRejection is identityRejection's item half: the movie, or the series.
func itemRejection(kind common.MediaKind, id Identity, want map[string]struct{}, parsed *release.ParsedRelease, rel common.ReleaseInfo) *common.Rejection {
	matched, conflict := compareIDs(kind, id.IDs, rel.IDs)
	if conflict != "" {
		r := newRejection(ReasonWrongItem, "%s", conflict)
		return &r
	}
	if matched {
		return nil
	}

	if rel.IndexerRef != "" && id.IDQueryIndexers[rel.IndexerRef] {
		if kind == common.MediaKindMovie {
			return movieYearRejection(id, parsed.Year)
		}
		return nil
	}

	got := releaseTitleKeys(parsed)
	switch {
	case len(id.Titles) == 0:
		r := newRejection(ReasonUnknownItem,
			"the item has no title yet, and the release neither carries one of its ids nor came from an id query")
		return &r
	case len(want) == 0:
		r := newRejection(ReasonUnknownItem,
			"none of the item's titles (primary %q) has anything the title comparison can read (release.CleanTitle keeps only ASCII letters and digits), and the release neither carries one of its ids nor came from an id query",
			id.Titles[0])
		return &r
	case len(got) == 0:
		r := newRejection(ReasonUnknownItem,
			"release title %q has nothing the title comparison can read (release.CleanTitle keeps only ASCII letters and digits), and the release neither carries one of the item's ids nor came from an id query",
			parsed.Title)
		return &r
	}

	for _, k := range got {
		if _, ok := want[k]; ok {
			if kind == common.MediaKindMovie {
				return movieYearRejection(id, parsed.Year)
			}
			return nil
		}
	}
	r := newRejection(ReasonWrongItem, "title %q matches none of the item's %d known title(s) (primary %q)",
		parsed.Title, len(id.Titles), id.Titles[0])
	return &r
}

// identityIDKeys are the id keys compared for each kind.
//
// An episode or pack compares the series tvdb id only. That is the key the
// search itself is built on (BuildSearchRequest's tvdbid) and the one the
// Newznab tvsearch attribute is defined over; a TV release's imdb or tmdb
// attribute is not reliably the SERIES' id -- a tracker can file an episode
// under the episode's own imdb page -- and comparing it against the series'
// would turn a correct release into a spurious conflict.
func identityIDKeys(kind common.MediaKind) []string {
	if kind == common.MediaKindMovie {
		return []string{common.IDKeyTMDB, common.IDKeyIMDB}
	}
	return []string{common.IDKeyTVDB}
}

// compareIDs compares every identityIDKeys key both sides carry. matched is
// true when at least one met and agreed; conflict names every key that met
// and disagreed, and when it is non-empty matched is false.
func compareIDs(kind common.MediaKind, target, rel map[string]string) (matched bool, conflict string) {
	var conflicts []string
	for _, k := range identityIDKeys(kind) {
		want, got := normalizeID(k, target[k]), normalizeID(k, rel[k])
		if want == "" || got == "" {
			continue
		}
		if want == got {
			matched = true
			continue
		}
		conflicts = append(conflicts, fmt.Sprintf("release %s id %s conflicts with the item's %s", k, rel[k], target[k]))
	}
	if len(conflicts) > 0 {
		return false, strings.Join(conflicts, "; ")
	}
	return matched, ""
}

// normalizeID reduces an id to its digits, so "tt0133093", "TT133093" and
// "133093" are one imdb id and "00603" is tmdb 603. Anything that is not an
// id at all -- empty, "0" (an indexer's "unknown", which Radarr also skips:
// FindMovie's `imdbId != "0"` and `tmdbId > 0`), or non-numeric -- normalizes
// to "", which compareIDs reads as absent rather than as a conflict.
func normalizeID(key, v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	if key == common.IDKeyIMDB {
		v = strings.TrimPrefix(v, "tt")
	}
	v = strings.TrimLeft(v, "0")
	for _, r := range v {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return v
}

// movieYearRejection is ruling R-7's year rule for a movie: the release's
// parsed year is the item's SecondaryYear exactly, or within
// movieYearTolerance of its Year. An unknown year on either side constrains
// nothing.
func movieYearRejection(id Identity, releaseYear int) *common.Rejection {
	if id.SecondaryYear >= minPlausibleYear && releaseYear == id.SecondaryYear {
		return nil
	}
	r := yearRejection(id.Year, releaseYear, movieYearTolerance)
	if r != nil && id.SecondaryYear >= minPlausibleYear {
		r.Reason += fmt.Sprintf(", and is not its secondary year %d", id.SecondaryYear)
	}
	return r
}

// yearRejection bounds a release's parsed year to within tolerance of the
// item's. An unknown year on either side constrains nothing.
func yearRejection(targetYear, releaseYear, tolerance int) *common.Rejection {
	if targetYear < minPlausibleYear || releaseYear < minPlausibleYear {
		return nil
	}
	d := targetYear - releaseYear
	if d < 0 {
		d = -d
	}
	if d <= tolerance {
		return nil
	}
	unit := "years"
	if tolerance == 1 {
		unit = "year"
	}
	r := newRejection(ReasonWrongItem, "release year %d is more than %d %s from the item's %d",
		releaseYear, tolerance, unit, targetYear)
	return &r
}

// umlauts is Radarr's ReplaceGermanUmlauts (Parser.cs), run before
// CleanTitle's accent stripping so "Schöne" keys as "schoene" -- the scene's
// own transliteration -- rather than "schone".
var umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss")

// titleStopWords are dropped wherever they occur, as Radarr's CleanMovieTitle
// NormalizeRegex drops a/an/the/and/or/of: "Fast & Furious" and "Fast and
// Furious" must key the same, and CleanTitle alone strips only a LEADING or
// TRAILING article and turns "&" into nothing.
var titleStopWords = map[string]bool{"a": true, "an": true, "the": true, "and": true, "or": true, "of": true}

// romanNumerals folds a standalone roman-numeral token into its arabic form,
// the job Radarr's _arabicRomanNumeralMappings does in
// TryGetMovieBySearchCriteria: "Rocky II" and "Rocky 2" are one film. It is
// applied to both sides alike, so folding a word that merely looks like a
// numeral ("I", "X") changes nothing about whether two titles agree.
var romanNumerals = map[string]string{
	"i": "1", "ii": "2", "iii": "3", "iv": "4", "v": "5", "vi": "6", "vii": "7", "viii": "8", "ix": "9", "x": "10",
	"xi": "11", "xii": "12", "xiii": "13", "xiv": "14", "xv": "15", "xvi": "16", "xvii": "17", "xviii": "18", "xix": "19", "xx": "20",
}

// titleKey is the comparison key for one title: release.CleanTitle (the
// package-wide equality form), with umlauts transliterated first, stop words
// dropped, roman numerals folded, and the spaces removed -- Radarr's clean
// titles carry none, which is what makes "Spider-Man" and "Spiderman" agree.
// "" means the title has nothing comparable in it.
func titleKey(s string) string {
	fields := strings.Fields(release.CleanTitle(umlauts.Replace(s)))
	kept := make([]string, 0, len(fields))
	for _, f := range fields {
		if titleStopWords[f] {
			continue
		}
		if n, ok := romanNumerals[f]; ok {
			f = n
		}
		kept = append(kept, f)
	}
	if len(kept) == 0 {
		// A title made only of stop words ("The Others" is not one, but
		// "A" or "The" alone could be): keep it rather than erase it.
		kept = fields
	}
	return strings.Join(kept, "")
}

// targetTitleKeys keys every title the item is known by. A series is also
// keyed as "<title><year>", because a disambiguated series is released under
// its title plus year ("Doctor.Who.2005.S01E01") and release.Parse leaves
// that year inside the series title.
func targetTitleKeys(kind common.MediaKind, id Identity) map[string]struct{} {
	keys := make(map[string]struct{}, 2*len(id.Titles))
	for _, t := range id.Titles {
		k := titleKey(t)
		if k == "" {
			continue
		}
		keys[k] = struct{}{}
		if kind != common.MediaKindMovie && id.Year > 0 {
			keys[fmt.Sprintf("%s%d", k, id.Year)] = struct{}{}
		}
	}
	return keys
}

// releaseTitleKeys keys every title the release was parsed as: Titles holds
// the primary title first plus any "AKA" or "/" alternate.
func releaseTitleKeys(parsed *release.ParsedRelease) []string {
	titles := parsed.Titles
	if len(titles) == 0 {
		titles = []string{parsed.Title}
	}
	out := make([]string, 0, len(titles))
	for _, t := range titles {
		if k := titleKey(t); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// numberingRejection is identityRejection's episode half: the release has to
// COVER the target's episodes. Three numberings are compared wherever both
// sides carry one -- in-season (SxxEyy, or a full-season pack of the season),
// anime absolute, and a daily series' air date -- and any one agreeing is
// enough, because the numberings legitimately disagree with each other
// (scene versus TVDB seasons for anime) while each is internally exact. None
// agreeing is WrongItem; none comparable at all is UnknownItem, failing
// closed for the same reason identityRejection's unevaluable case does.
//
// A full-season pack of the right season covers every episode of it, so the
// numbering half accepts one for any target. Whether a single-episode search
// SHOULD take a whole season is the separate question seasonPackRejection
// answers, as Sonarr answers it in a separate specification.
func numberingRejection(id Identity, p *release.ParsedRelease) *common.Rejection {
	var (
		compared   bool
		mismatches []string
	)
	if len(id.Episodes) > 0 && len(p.Seasons) > 0 && (p.FullSeason || len(p.Episodes) > 0) {
		compared = true
		if coversInSeason(id, p) {
			return nil
		}
		mismatches = append(mismatches, fmt.Sprintf("release is %s, the item is %s", describeRelease(p), describeTarget(id)))
	}
	if len(id.Absolute) > 0 && len(p.Absolute) > 0 {
		compared = true
		if containsAll(p.Absolute, id.Absolute) {
			return nil
		}
		mismatches = append(mismatches, fmt.Sprintf("release is absolute %v, the item is absolute %v", p.Absolute, id.Absolute))
	}
	if id.AirDate != nil && p.AirDate != nil {
		compared = true
		if sameDay(*id.AirDate, *p.AirDate) {
			return nil
		}
		mismatches = append(mismatches, fmt.Sprintf("release aired %s, the item aired %s",
			p.AirDate.UTC().Format(time.DateOnly), id.AirDate.UTC().Format(time.DateOnly)))
	}
	if compared {
		r := newRejection(ReasonWrongItem, "%s", strings.Join(mismatches, "; "))
		return &r
	}
	r := newRejection(ReasonUnknownItem,
		"the release names no season/episode, absolute number or air date the item also has (release is %s, item is %s)",
		describeRelease(p), describeTarget(id))
	return &r
}

func coversInSeason(id Identity, p *release.ParsedRelease) bool {
	if !containsAll(p.Seasons, []int{id.Season}) {
		return false
	}
	return p.FullSeason || containsAll(p.Episodes, id.Episodes)
}

func containsAll(haystack, needles []int) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func sameDay(a, b time.Time) bool {
	a, b = a.UTC(), b.UTC()
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// describeRelease and describeTarget render numbering for a rejection
// message: "S01E05", "S01E05E06", "S01 full season", "absolute [12]".
func describeRelease(p *release.ParsedRelease) string {
	var b strings.Builder
	for i, s := range p.Seasons {
		if i > 0 {
			b.WriteString("-")
		}
		fmt.Fprintf(&b, "S%02d", s)
	}
	if p.FullSeason {
		b.WriteString(" full season")
	}
	for _, e := range p.Episodes {
		fmt.Fprintf(&b, "E%02d", e)
	}
	if len(p.Absolute) > 0 {
		sep(&b)
		fmt.Fprintf(&b, "absolute %v", p.Absolute)
	}
	if p.AirDate != nil {
		sep(&b)
		b.WriteString("aired " + p.AirDate.UTC().Format(time.DateOnly))
	}
	if b.Len() == 0 {
		return "unnumbered"
	}
	return b.String()
}

func describeTarget(id Identity) string {
	var b strings.Builder
	if len(id.Episodes) > 0 {
		fmt.Fprintf(&b, "S%02d", id.Season)
		for _, e := range id.Episodes {
			fmt.Fprintf(&b, "E%02d", e)
		}
	}
	if len(id.Absolute) > 0 {
		sep(&b)
		fmt.Fprintf(&b, "absolute %v", id.Absolute)
	}
	if id.AirDate != nil {
		sep(&b)
		b.WriteString("aired " + id.AirDate.UTC().Format(time.DateOnly))
	}
	if b.Len() == 0 {
		return "unnumbered"
	}
	return b.String()
}

func sep(b *strings.Builder) {
	if b.Len() > 0 {
		b.WriteString(", ")
	}
}
