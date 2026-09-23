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
	// FreeBytes is carried per spec §7; unused by this task -- see
	// Disagreement 5. A later task may wire up a free-space check against it.
	FreeBytes int64
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
