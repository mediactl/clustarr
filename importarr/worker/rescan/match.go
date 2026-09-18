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

package rescan

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/release"
)

// MaxCandidates caps the candidate list recorded against an unmatched file.
// It is catalogv1alpha1.UnmatchedFile.Candidates' own MaxItems: a list the
// apiserver would reject is worse than a truncated one.
const MaxCandidates = 10

// Reason codes for an unmatched file. They are deliberately short, closed and
// free of any path, title or release name, because they are the `reason`
// label on clustarr_import_unmatched_total -- a label that took a human
// string would be unbounded cardinality. The human-readable sentence lives in
// [MatchResult.Reason] and reaches LibraryScan.status.unmatched; only the
// code reaches Prometheus.
const (
	// CodeParseError means pkg/release could not parse the filename at all.
	CodeParseError = "parse_error"

	// CodeUnresolvedID means the file carried a provider id that the
	// metadata gateway could not turn into a catalog identity.
	CodeUnresolvedID = "unresolved_id"

	// CodeAmbiguousTitle means two or more existing items share the title
	// the filename parsed to.
	CodeAmbiguousTitle = "ambiguous_title"

	// CodeNoMatch means the file carried no provider id and no existing
	// item shared its title.
	CodeNoMatch = "no_match"

	// CodeUnsupportedKind means the file sits under a root folder whose
	// kind library rescan does not handle yet.
	CodeUnsupportedKind = "unsupported_root_kind"

	// CodeNoQualityProfile means a new item would have to be created but
	// the root folder names no default QualityProfile, which Movie.spec
	// requires.
	CodeNoQualityProfile = "no_default_quality_profile"
)

// MovieCandidate is one existing Movie, reduced to what matching needs. The
// worker builds these by listing Movies in the scan's namespace; MatchMovie
// itself touches no cluster, so every "must not match" case is testable as a
// pure function.
type MovieCandidate struct {
	// Name is the Movie object's name.
	Name string

	// TmdbID is Movie.spec.tmdbID, the immutable identity.
	TmdbID int64

	// Title is the movie's display title, from status.metadata when the
	// gateway has filled it in.
	Title string

	// Year is the movie's release year, from status.metadata; zero when
	// unknown.
	Year int
}

// ResolveIMDb turns an IMDb id into a TMDB id, normally by asking the
// metadata gateway over RPC. It returns an error when the id cannot be
// resolved, which [MatchMovie] treats as "do not match" rather than "try
// something looser".
type ResolveIMDb func(imdbID string) (tmdbID int64, err error)

// MatchResult is [MatchMovie]'s verdict on one file.
type MatchResult struct {
	// TmdbID is the resolved identity. It is set whenever matching
	// succeeded, whether against an existing Movie or as the identity a new
	// one should be created with.
	TmdbID int64

	// ExistingName names the Movie that was matched. Empty with Unmatched
	// false means the caller must create a Movie for TmdbID.
	ExistingName string

	// Unmatched is true when the file could not be attributed with
	// confidence. The scanner never guesses: the caller records it in
	// LibraryScan.status.unmatched instead of inventing an item.
	Unmatched bool

	// Code is the bounded reason code, one of the Code* constants. It is
	// safe as a metric label.
	Code string

	// Reason is the human-readable explanation surfaced on the resource.
	Reason string

	// Candidates names the items the scanner considered but could not
	// choose between, capped at [MaxCandidates].
	Candidates []string
}

// MatchMovie decides which Movie a parsed file belongs to, in the order
// amendment §A1.5 lays out and no further:
//
//  1. an embedded tmdb id is the identity, full stop;
//  2. an embedded imdb id is resolved through the metadata gateway, and a
//     resolution failure is an unmatched file, not a fallback to the title;
//  3. otherwise the cleaned title -- plus the year when both sides know one
//     -- is compared against the existing movies, and only an unambiguous
//     single hit matches.
//
// What it deliberately does not do is resolve a bare, id-less title by
// searching the metadata gateway. §A1.5's "then against the metadata
// gateway" is read here as covering step 2's id resolution only: turning an
// arbitrary filename into a brand-new catalog item on the strength of a
// provider search is the single riskiest form of guessing the never-guess
// rule exists to forbid. A file like that is reported as unmatched with its
// candidates, and a human decides.
//
// It is pure: no cluster, no clock, and the only outbound call is resolve,
// which is injected.
func MatchMovie(parsed *release.ParsedRelease, existing []MovieCandidate, resolve ResolveIMDb) MatchResult {
	if parsed == nil {
		return MatchResult{
			Unmatched: true,
			Code:      CodeParseError,
			Reason:    "the filename could not be parsed into a release",
		}
	}

	if id := tmdbID(parsed); id > 0 {
		return matchByTmdbID(id, existing)
	}

	if imdbID := strings.TrimSpace(parsed.IDs["imdb"]); imdbID != "" {
		if resolve == nil {
			return MatchResult{
				Unmatched: true,
				Code:      CodeUnresolvedID,
				Reason:    fmt.Sprintf("imdb id %q needs the metadata gateway to resolve and no resolver is configured", imdbID),
			}
		}
		id, err := resolve(imdbID)
		if err != nil || id <= 0 {
			reason := fmt.Sprintf("the metadata gateway could not resolve imdb id %q", imdbID)
			if err != nil {
				reason = fmt.Sprintf("%s: %v", reason, err)
			}
			return MatchResult{Unmatched: true, Code: CodeUnresolvedID, Reason: reason}
		}
		return matchByTmdbID(id, existing)
	}

	return matchByTitle(parsed, existing)
}

// tmdbID reads the tmdb id pkg/release extracted from the filename or any
// ancestor directory. A non-numeric or non-positive value is treated as
// absent rather than as a match on zero.
func tmdbID(parsed *release.ParsedRelease) int64 {
	raw := strings.TrimSpace(parsed.IDs["tmdb"])
	if raw == "" {
		return 0
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

// matchByTmdbID binds a known identity to an existing Movie when one carries
// it, and otherwise reports the identity a new Movie should be created with.
func matchByTmdbID(id int64, existing []MovieCandidate) MatchResult {
	for _, c := range existing {
		if c.TmdbID == id {
			return MatchResult{TmdbID: id, ExistingName: c.Name}
		}
	}
	return MatchResult{TmdbID: id}
}

// matchByTitle is step 3: exact cleaned-title equality against the existing
// items, narrowed by year when both sides know one. Zero hits and two-or-more
// hits are both unmatched -- the second is the case a looser matcher would
// silently get wrong.
func matchByTitle(parsed *release.ParsedRelease, existing []MovieCandidate) MatchResult {
	want := release.CleanTitle(parsed.Title)
	if want == "" {
		return MatchResult{
			Unmatched: true,
			Code:      CodeParseError,
			Reason:    "no title could be parsed from the filename and it carries no provider id",
		}
	}

	var hits []MovieCandidate
	for _, c := range existing {
		if release.CleanTitle(c.Title) != want {
			continue
		}
		// The year only narrows when both sides actually know one; a
		// yearless filename must not be excluded by a movie's year, nor
		// the other way round.
		if parsed.Year != 0 && c.Year != 0 && parsed.Year != c.Year {
			continue
		}
		hits = append(hits, c)
	}

	switch len(hits) {
	case 1:
		return MatchResult{TmdbID: hits[0].TmdbID, ExistingName: hits[0].Name}
	case 0:
		return MatchResult{
			Unmatched: true,
			Code:      CodeNoMatch,
			Reason: fmt.Sprintf(
				"no embedded provider id and no confident title match among %d existing movies",
				len(existing)),
		}
	default:
		names := make([]string, 0, len(hits))
		for _, c := range hits {
			names = append(names, c.Name)
		}
		slices.Sort(names)
		if len(names) > MaxCandidates {
			names = names[:MaxCandidates]
		}
		return MatchResult{
			Unmatched:  true,
			Code:       CodeAmbiguousTitle,
			Reason:     fmt.Sprintf("ambiguous: %d existing movies share title %q", len(hits), parsed.Title),
			Candidates: names,
		}
	}
}
