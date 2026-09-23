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
	"time"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// Current is the quality-model state of the file already imported for a
// Target, when one exists. SourceTitle mirrors the real
// catalogv1alpha1.MediaFileSpec.ImportedFrom.ReleaseTitle field verbatim;
// SourceHash has no MediaFile-side source (a torrent's info hash lives on
// the Download that produced the file, not on MediaFile itself) and is the
// caller's responsibility to resolve from that Download when one still
// exists -- see Disagreement 3.
type Current struct {
	Quality     common.Quality
	Revision    common.Revision
	FormatScore int
	Formats     []string
	SourceHash  string
	SourceTitle string
}

// Queued is a release already downloading for a Target. It is a type alias
// for quality.Candidate (Disagreement 4): spec §7 names the field
// Target.Queue []Queued without ever defining Queued, and quality.Candidate
// is exactly the shape queueRejection needs to call p.UpgradeDecision.
type Queued = quality.Candidate

// Target is everything about one catalog item Evaluate needs that isn't
// carried on a candidate release itself. Blocklist matches on infohash
// (torrent) or title (usenet) -- docs/research/naming.md §A7.
type Target struct {
	Kind            common.MediaKind
	Key             string
	Monitored       bool
	Available       bool
	RuntimeMinutes  int
	EpisodeRuntimes []int
	// OriginalLanguageTag is the item's original language as a BCP-47 tag,
	// verbatim from Movie.status.metadata.originalLanguage or its Series
	// twin -- "en", "ja", "pt-BR". NOT a display name: the conversion into
	// the English-display-name vocabulary release.ParsedRelease.Languages
	// and the TRaSH catalogue speak happens inside Evaluate, exactly once,
	// in originalLanguageName (language.go). A caller that copies the CRD
	// field straight across is correct; that is the only thing a caller
	// should do. Empty, or a tag the table does not carry, means "unknown"
	// and constrains nothing.
	OriginalLanguageTag string
	Current             *Current
	Queue               []Queued
	Blocklist           func(infohash, title string) bool
	// FreeBytes is read by nothing. Spec §7 listed it; gap fix X13 struck it
	// there, because free space is judged at import against
	// RootFolder.minFreeBytes and by the RootFolder's DiskSpaceOK condition,
	// not by the release decision (Disagreement 5). A prune candidate.
	FreeBytes int64
	// Identity is WHICH item this is: what a candidate release has to be for
	// before anything else about it matters (identity.go). The zero value
	// identifies nothing, and every release evaluated against it is rejected
	// as ReasonUnknownItem -- deliberately, so a construction site that
	// forgets to fill it fails loudly (nothing is approved) instead of
	// approving whatever an indexer happened to return.
	Identity Identity
}

// Identity is what the identity check (identity.go, identity_nonvideo.go)
// compares a candidate release against. Every field comes from the catalog
// item's own spec and status; nothing here is derived from a release.
type Identity struct {
	// Titles is every title the item is known by, primary first: for a movie
	// status.metadata.title, originalTitle and alternateTitles; for an
	// episode or a pack, the owning SERIES' title and alternate titles (an
	// Episode has no title a release would carry); for an album its title;
	// for a book or audiobook its title, and its "Title: Subtitle" form when
	// it has a subtitle; for an issue the owning COMIC's (volume's) title --
	// an issue's own title is not what a release names. Empty until
	// metadata lands.
	Titles []string
	// Year is the one year the item is known by, 0 when unknown: a movie's
	// status.metadata.year, a series' first-aired year, an album's release
	// year (status.metadata.releaseDate), an issue's cover-date year
	// (status.date). For a movie, an album and an issue it bounds the
	// release's parsed year (movieYearRejection, albumYearTolerance,
	// issueYearTolerance); for a series it is used only to recognise the
	// "Doctor Who 2005" disambiguated-title form. A book or audiobook
	// ignores it: editions span decades, and Readarr matches no year either.
	Year int
	// SecondaryYear is a movie's second provider-sourced year,
	// status.metadata.secondaryYear: the year a festival premiere and a
	// general release, or two regions, disagree on. 0 means there is none. A
	// release named by it is the same film however far it sits from Year
	// (movieYearRejection). Unused for every other kind.
	SecondaryYear int
	// IDs are the item's external ids, keyed by commonv1.IDKeyTMDB /
	// IDKeyIMDB / IDKeyTVDB exactly as ReleaseInfo.IDs is: tmdb (spec) and
	// imdb (status.metadata.externalIDs) for a movie; the SERIES' tvdb id for
	// an episode or a pack. A key the item does not have is simply absent.
	IDs map[string]string
	// Season and Episodes are the in-season numbering the target covers: one
	// episode for a single-episode search, every episode of a pack for an RSS
	// pack target. Season is meaningful only when Episodes is non-empty
	// (season 0 is a real season -- specials).
	Season   int
	Episodes []int
	// Absolute is the anime absolute numbering of the same episodes, empty
	// when the catalog has none.
	Absolute []int
	// AirDate is a single episode's air date, the only numbering a daily
	// series' releases carry. Nil for a pack or when not yet known.
	AirDate *time.Time
	// SceneMappings is the SERIES' scene-numbering table (TheXEM's, for an
	// anime series whose scene seasons or absolute numbers differ from
	// TVDB's). The numbering half reads a release's numbers through it first
	// and literally only where it has no row, so a scene-numbered release is
	// compared as the TVDB episode it names (releaseCoverage). Nil means no
	// mapping: every number is read literally.
	//
	// It must be the whole series' table, not only the target's rows. A
	// release numbered by a scene number that maps to a DIFFERENT episode is
	// that other episode, and only its row says so: with the row missing,
	// the number would fall back to its literal reading and could match the
	// target by coincidence.
	SceneMappings []SceneMapping
	// IDQueryIndexers names (by ReleaseInfo.IndexerRef) the indexers whose
	// query for THIS search was keyed by one of the item's ids
	// (schema.SearchQueryModeID), i.e. the indexer matched the id server-side.
	// A release from one of them that carries no ids of its own is treated as
	// id-identified -- see identityRejection for why, and for the one check
	// such a release still has to pass. Nil for an RSS decision: a firehose
	// release was not found by any query at all.
	//
	// It is per indexer, not one flag for the Target, because a federated
	// search mixes modes: each indexer falls back to a text query
	// independently (SearchOutcome.QueryMode), so one search's releases can be
	// part id-found and part text-found.
	IDQueryIndexers map[string]bool
	// SingleEpisodeSearch is true when the candidates answer a search for
	// exactly one episode -- the search worker's episode search, automatic or
	// interactive. A full-season pack of the right season is then rejected
	// as ReasonFullSeason (ruling R-3, Sonarr's
	// SingleEpisodeSearchMatchSpecification), because a whole season is not
	// what was asked for. False for everything else: an RSS decision was not
	// asked for anything, and keeps accepting a pack that covers the
	// episodes it was matched to, as Sonarr's RSS path does.
	//
	// It is a property of the search, like IDQueryIndexers, so the caller
	// that runs the search sets it; EpisodeIdentity cannot infer it from
	// "one episode", because an RSS pack target can resolve to one episode
	// too.
	SingleEpisodeSearch bool

	// Creators names who made a non-video item, primary first: an album's
	// artist (the owning Artist's name and aliases), a book's or
	// audiobook's authors. A release must name one of them
	// (identity_nonvideo.go). Unused for video and for an issue.
	Creators []string
	// Issue is an issue target's number exactly as Issue.spec.number carries
	// it ("12", "12.5", "Annual 1"); for a manga Comic, the chapter number.
	// Compared numerically where both sides are numbers ("050" is 50), and
	// as text otherwise. Unused for every other kind.
	Issue string
}

// Options carries the parts of a decision that come from something other
// than the release or the quality profile: whether this is a user-invoked
// (interactive) search, which protocols are currently enabled, indexer
// priority, and the protocol Evaluate/Rank should prefer when the profile
// itself doesn't say (Profile.PreferredProtocol is the fallback -- see
// buildRankKey).
type Options struct {
	UserInvoked       bool
	ProtocolsEnabled  map[string]bool // missing key = disabled (fail closed); caller populates both "torrent" and "usenet"
	IndexerPriority   map[string]int  // missing key defaults to 25, docs/research/indexers.md's documented default
	PreferredProtocol string
}

// RankKey is the part of a Decision's rank Evaluate can compute (it alone
// has Profile and Target); Rank combines it with the parts that only need
// Options and the Decision's own Release/Parsed at sort time. Only
// populated when Approved is true.
type RankKey struct {
	QualityIndex           int
	PreferRevision         bool // false when Profile.ProperPolicy == "doNotPrefer"
	Revision               common.Revision
	FormatScore            int
	PreferredProtocolMatch bool
	EpisodeCount           int   // 1 for a single release; len(Parsed.Episodes) for a partial multi-episode release; a season-pack sentinel for FullSeason
	PreferLargestSize      bool  // true when the quality's preferred size is TRaSH's "biggest" sentinel (Step 13)
	SizeDeltaBucket        int64 // |release.SizeBytes - preferredBytes|, rounded to 200 MiB; meaningful only when !PreferLargestSize
	SizeBytes              int64 // meaningful only when PreferLargestSize
}

// Decision is one release's verdict against one Target. Its shape matches
// spec §7's literal Decision struct field-for-field.
type Decision struct {
	Release             common.ReleaseInfo
	Parsed              *release.ParsedRelease // nil only when Release.Title failed to parse (ReasonUnableToParse)
	Approved            bool                   // len(Rejections) == 0
	TemporarilyRejected bool                   // len(Rejections) > 0 && every Rejection is common.RejectionTemporary
	Rejections          []common.Rejection
	Score               int
	Matched             []string
	Rank                RankKey
}

// Reason is a typed, non-string rejection reason: a stable Code ported from
// Radarr/Sonarr's verified DownloadRejectionReason enum
// (docs/research/naming.md §A6) plus the RejectionType every use of it
// carries, so a call site cannot forget to mark whether Search.spec.override
// is required. See reasons.go for the full list; every one of this
// package's Reason values is common.RejectionPermanent (Disagreement 6).
type Reason struct {
	Code string
	Type common.RejectionType
}
