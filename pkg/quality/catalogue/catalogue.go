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

package catalogue

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dlclark/regexp2"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// trashMatchTimeout is spec §9's fixed 50ms budget: "MatchTimeout 50 ms;
// timeout = no match + log".
const trashMatchTimeout = 50 * time.Millisecond

// compileTRaSH compiles a TRaSH ReleaseTitle/ReleaseGroup pattern the way
// *arr does: case-insensitive, backtracking (.NET-compatible) regex, with a
// timeout so a catastrophic pattern degrades to "no match" instead of
// hanging a reconcile or search worker.
func compileTRaSH(pattern string) (*regexp2.Regexp, error) {
	re, err := regexp2.Compile(pattern, regexp2.IgnoreCase)
	if err != nil {
		return nil, err
	}
	re.MatchTimeout = trashMatchTimeout
	return re, nil
}

// matchFailureMessage picks the log message for a regexp2 MatchString
// failure. A MatchTimeout is the expected one, but it is not the only
// possible error and regexp2 exports no sentinel for it -- the timeout is a
// plain fmt.Errorf("match timeout after %v on input `%v`", ...) built in
// runner.go -- so the message is selected from that text rather than
// asserting every failure is a timeout.
func matchFailureMessage(err error) string {
	if err != nil && strings.Contains(err.Error(), "match timeout") {
		return "regexp2 match timed out"
	}
	return "regexp2 match failed"
}

// matchTRaSH runs re against s, treating any match failure as "no match"
// rather than an error, and logging it through ctx so an operator can find
// the pathological pattern without the caller having to thread an error
// return through every Condition kind.
func matchTRaSH(ctx context.Context, re *regexp2.Regexp, name, s string) bool {
	ok, err := re.MatchString(s)
	if err != nil {
		logging.FromContext(ctx).Warn(matchFailureMessage(err),
			"condition", name, "timeout", trashMatchTimeout, "err", err)
		return false
	}
	return ok
}

// MatchTRaSHForTest exposes matchTRaSH to tests in catalogue_test (external
// test package); production callers never need it directly, only through
// Match/Score.
func MatchTRaSHForTest(ctx context.Context, re *regexp2.Regexp, name, s string) bool {
	return matchTRaSH(ctx, re, name, s)
}

// CondKind is the kind of fact a Condition inspects. Edition, Size and Year
// are accepted (the loader does not reject them) but never evaluated --
// spec §9: "Edition/Size/Year are accepted no-ops."
type CondKind string

// The *arr CustomFormatSpecification kinds this package understands.
const (
	CondReleaseTitle CondKind = "ReleaseTitle"
	CondReleaseGroup CondKind = "ReleaseGroup"
	CondSource       CondKind = "Source"
	CondResolution   CondKind = "Resolution"
	CondModifier     CondKind = "Modifier"
	CondLanguage     CondKind = "Language"
	CondIndexerFlag  CondKind = "IndexerFlag"
	CondReleaseType  CondKind = "ReleaseType"
	CondEdition      CondKind = "Edition" // no-op
	CondSize         CondKind = "Size"    // no-op
	CondYear         CondKind = "Year"    // no-op
)

// Condition is one *arr CustomFormatSpecification. Exactly one of Pattern,
// Source, Resolution, Modifier, (Language+ExceptLanguage), Flag or
// ReleaseType is meaningful, selected by Kind.
type Condition struct {
	Kind           CondKind
	Name           string
	Negate         bool
	Required       bool
	Pattern        *regexp2.Regexp    // CondReleaseTitle, CondReleaseGroup
	Source         common.Source      // CondSource
	Resolution     int32              // CondResolution
	Modifier       common.Modifier    // CondModifier
	Language       string             // CondLanguage; "Original" (languageByID[-2]) resolves against ItemContext.OriginalLanguage. Otherwise an English display name from languageByID (languages.go), matching release.ParsedRelease.Languages' own vocabulary -- never an ISO code.
	ExceptLanguage bool               // CondLanguage
	Flag           string             // CondIndexerFlag
	ReleaseType    common.ReleaseType // CondReleaseType
}

// Format is one custom format: a scored, named group of Conditions.
type Format struct {
	Slug       string
	Name       string
	TrashIDs   map[string]string // app -> trash_id, e.g. {"radarr": "...", "sonarr": "..."}
	Scores     map[string]int    // score set name -> score; "default" always present
	Group      string            // "" (always active) or one of catalogv1alpha1's FormatGroup* constants
	Conditions []Condition
}

// Catalogue is the full set of loaded custom formats.
type Catalogue struct {
	Version   string
	Formats   map[string]*Format // slug -> Format
	Conflicts [][2]string        // pairs of slugs that must not both be enabled; empty in this bootstrap set
}

// ItemContext supplies the facts a Condition needs that are not on the
// release itself: the catalog item's original language, the indexer's flags
// on this specific release, and the release type as classified relative to
// what it is being matched against (may differ from
// release.ParsedRelease.ReleaseType -- e.g. a season pack matched against one
// missing episode).
type ItemContext struct {
	// OriginalLanguage must use the same English-display-name vocabulary as
	// release.ParsedRelease.Languages (see languages.go's languageByID doc
	// comment) -- e.g. "Japanese", not "ja" or "jpn" -- for a "language ==
	// original" Condition to ever match.
	OriginalLanguage string
	IndexerFlags     []string
	ReleaseType      common.ReleaseType
}

// evalCondition applies Negate to the raw match, per spec §9: "Negate per
// condition". CondEdition/CondSize/CondYear are accepted no-ops: they always
// report false raw match with no effect on their group (formatMatches'
// group-then-all-groups rule means an all-no-op group with no Required
// member simply never contributes "ok", which is why every embedded no-op
// condition in this catalogue always shares its Kind-group with at least one
// evaluated condition -- see docs/research/quality.md §9).
func evalCondition(ctx context.Context, c Condition, r *release.ParsedRelease, ic ItemContext) bool {
	var raw bool
	switch c.Kind {
	case CondReleaseTitle:
		raw = matchTRaSH(ctx, c.Pattern, c.Name, r.Title)
	case CondReleaseGroup:
		raw = matchTRaSH(ctx, c.Pattern, c.Name, r.Group)
	case CondSource:
		raw = r.Quality.Source == c.Source
	case CondResolution:
		raw = r.Quality.Resolution == c.Resolution
	case CondModifier:
		raw = r.Quality.Modifier == c.Modifier
	case CondLanguage:
		// TODO(generator sync): ExceptLanguage is reserved for a future
		// per-language-exception list the note mentions but the curated
		// data set does not use yet; it is decoded onto Condition but not
		// read here.
		want := c.Language
		if want == "Original" {
			want = ic.OriginalLanguage
		}
		raw = slices.Contains(r.Languages, want)
	case CondIndexerFlag:
		raw = slices.Contains(ic.IndexerFlags, c.Flag)
	case CondReleaseType:
		raw = ic.ReleaseType == c.ReleaseType
	case CondEdition, CondSize, CondYear:
		raw = false
	default:
		raw = false
	}
	if c.Negate {
		return !raw
	}
	return raw
}

// Match implements docs/research/quality.md §4.2's SpecificationMatchesGroup
// algorithm verbatim: group Conditions by Kind; a Kind-group is ok iff no
// Required Condition in it failed AND at least one Condition in it matched;
// a Format matches iff every Kind-group present on it is ok. A Format with
// no Conditions of a given Kind simply has no group for that Kind (it is not
// evaluated, and cannot fail).
//
// ctx is the one place this package's signatures are not a literal
// transcription of spec §7's one-line summary (`Match(r, ic) []string`,
// no context): CLAUDE.md's logging invariant ("no package-level logger, no
// logger struct fields") leaves context as the only place a regexp2 timeout
// can be logged (spec §9: "timeout = no match + log"), so ctx is threaded
// through as Match/Score's first parameter. This is additive (one
// parameter), not a shape change.
func (c *Catalogue) Match(ctx context.Context, r *release.ParsedRelease, ic ItemContext) []string {
	var slugs []string
	for slug, f := range c.Formats {
		if formatMatches(ctx, f, r, ic) {
			slugs = append(slugs, slug)
		}
	}
	sort.Strings(slugs)
	return slugs
}

func formatMatches(ctx context.Context, f *Format, r *release.ParsedRelease, ic ItemContext) bool {
	groups := map[CondKind][]Condition{}
	for _, cond := range f.Conditions {
		groups[cond.Kind] = append(groups[cond.Kind], cond)
	}
	for _, conds := range groups {
		ok := false
		failedRequired := false
		for _, cond := range conds {
			matched := evalCondition(ctx, cond, r, ic)
			if matched {
				ok = true
			}
			if cond.Required && !matched {
				failedRequired = true
			}
		}
		if failedRequired || !ok {
			return false
		}
	}
	return true
}

// Score sums scores[slug] for every slug Match returns (0 for a matched
// slug absent from scores, i.e. an optional format group the profile did
// not enable), and returns the matched slugs alongside the total. This
// takes a plain map rather than *quality.Profile because pkg/quality
// imports pkg/quality/catalogue -- a *Profile parameter here would be an
// import cycle. pkg/quality.Profile.Score is the *Profile-shaped wrapper
// spec §7 actually shows.
func (c *Catalogue) Score(ctx context.Context, scores map[string]int, r *release.ParsedRelease, ic ItemContext) (score int, matched []string) {
	matched = c.Match(ctx, r, ic)
	for _, slug := range matched {
		score += scores[slug]
	}
	return score, matched
}
