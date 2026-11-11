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

package schema

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Candidate origins (Candidate.Origin).
const (
	CandidateOriginRSS         = "rss"
	CandidateOriginSearch      = "search"
	CandidateOriginInteractive = "interactive"
)

// Candidate is a release proposed for an owner's grab, on the
// CLUSTARR_INTAKE stream at events.IntakeCandidateSubject, consumed by the
// manager's catalog-intake-candidate (ADR-0019 §4.3, §6.6): the RSS
// matcher's matches and a Search's picks. The manager's grab planner
// decides; the publisher decides nothing.
type Candidate struct {
	// Owner is the item whose status.downloads would hold the grab: the
	// Series or Comic for an episode or issue.
	Owner ItemRef `json:"owner"`
	// Targets are the matched Episodes or Issues, when the owner is a
	// container.
	Targets []ItemRef `json:"targets,omitempty"`
	// Origin is CandidateOriginRSS, CandidateOriginSearch or
	// CandidateOriginInteractive.
	Origin string `json:"origin"`
	// SearchRef is the Search a pick came from.
	SearchRef *Ref `json:"searchRef,omitempty"`
	// Manual marks a person's pick; Override a permanent rejection the
	// person accepted.
	Manual    bool                     `json:"manual,omitempty"`
	Override  bool                     `json:"override,omitempty"`
	GrabbedBy commonv1.GrabSource      `json:"grabbedBy"`
	Purpose   commonv1.DownloadPurpose `json:"purpose,omitempty"`
	Release   ScoredRelease            `json:"release"`
	At        time.Time                `json:"at"`
	// Season is the season an interactive season search's pick was found
	// for (Wave S): the owner resolves what it covers from the release's
	// coverage, and without one from the season's episodes its parsed
	// numbers name.
	Season *int32 `json:"season,omitempty"`
}

// Schema implements Payload.
func (Candidate) Schema() string { return "catalog.Candidate.v1" }

// ParsedFacts are the facts the release parser read from a title or a file
// name: what it covers.
type ParsedFacts struct {
	Seasons  []int32 `json:"seasons,omitempty"`
	Episodes []int32 `json:"episodes,omitempty"`
	Absolute []int32 `json:"absolute,omitempty"`
	// AirDate is a daily episode's date, "2006-01-02".
	AirDate     string `json:"airDate,omitempty"`
	FullSeason  bool   `json:"fullSeason,omitempty"`
	MultiSeason bool   `json:"multiSeason,omitempty"`
	Issue       string `json:"issue,omitempty"`
	// Coverage is what the release covers in the series' stored numbering,
	// after its scene mapping (decision.CoverageOf); nil for a non-episode
	// kind or an unparsed title. Each of its lists holds at most
	// MaxCoverageNumbers entries.
	Coverage *ReleaseCoverage `json:"coverage,omitempty"`
}

// MaxCoverageNumbers caps each list of a ReleaseCoverage.
const MaxCoverageNumbers = 1000

// ReleaseCoverage is a release's coverage in a series' stored (TVDB)
// numbering, read through its TheXEM scene table first (Wave S): whole
// seasons it is a pack of (read literally: a pack of a scene season is its
// TVDB episodes), the season and episode pairs it names, and the absolute
// numbers it names. At most MaxCoverageNumbers entries in each list.
type ReleaseCoverage struct {
	FullSeasons []int32         `json:"fullSeasons,omitempty"`
	Episodes    []SeasonEpisode `json:"episodes,omitempty"`
	Absolute    []int32         `json:"absolute,omitempty"`
}

// SeasonEpisode is one episode by season and number.
type SeasonEpisode struct {
	Season  int32 `json:"season"`
	Episode int32 `json:"episode"`
}

// ScoredRelease is one release an agent evaluated against an item's
// profile and identity (ADR-0019 §7.4): the decision with its rejections,
// what it covers, its block state from the release index (§6.14) and its
// score.
type ScoredRelease struct {
	Decision commonv1.ReleaseDecision `json:"decision"`
	Parsed   ParsedFacts              `json:"parsed"`
	// Blocked is the release index's block for the request's scope or the
	// global one.
	Blocked         *ReleaseBlock `json:"blocked,omitempty"`
	Score           int32         `json:"score,omitempty"`
	IndexerPriority int32         `json:"indexerPriority,omitempty"`
}

// Scan observation kinds (ScanObservation.Kind).
const (
	ScanObservationPresent    = "present"
	ScanObservationMissing    = "missing"
	ScanObservationUnmatched  = "unmatched"
	ScanObservationOrphanPart = "orphanPart"
)

// ScanObservation is one file a library scan saw that differs from the
// MediaFile the agent read, or that has none, on the CLUSTARR_INTAKE stream
// at events.IntakeScanSubject, consumed by the manager's
// import-intake-scan (ADR-0019 §7.6). The manager's scan applier
// decides; the rescan writes nothing.
type ScanObservation struct {
	Scan       Ref `json:"scan"`
	RootFolder Ref `json:"rootFolder"`
	// Kind is ScanObservationPresent, Missing, Unmatched or OrphanPart.
	Kind        string    `json:"kind"`
	Path        string    `json:"path"`
	SizeBytes   int64     `json:"sizeBytes,omitempty"`
	ModTime     time.Time `json:"modTime,omitzero"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	// Attribution is the proposed item, with its evidence; the scanner
	// never guesses, so an ambiguous one arrives as Unmatched.
	Attribution *ScanAttribution `json:"attribution,omitempty"`
	// Frozen are the import fields the agent would freeze into a new
	// MediaFile.
	Frozen *FrozenFields `json:"frozen,omitempty"`
	// Basis is the MediaFile the agent read; a stale basis is re-observed,
	// never applied.
	Basis     *MediaFileBasis `json:"basis,omitempty"`
	Unmatched *UnmatchedFact  `json:"unmatched,omitempty"`
	// ProfileTag is the file's CLUSTARR_PROFILE tag, TranscodeOutputOf the
	// source a transcode output belongs to.
	ProfileTag        string       `json:"profileTag,omitempty"`
	TranscodeOutputOf string       `json:"transcodeOutputOf,omitempty"`
	Missing           *MissingFact `json:"missing,omitempty"`
	// Manual is a LibraryScan's manual assignment.
	Manual bool      `json:"manual,omitempty"`
	SeenAt time.Time `json:"seenAt"`
}

// Schema implements Payload.
func (ScanObservation) Schema() string { return "import.ScanObservation.v1" }

// ScanAttribution is the item a scanned file belongs to.
type ScanAttribution struct {
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	ProviderID string   `json:"providerID,omitempty"`
	Keys       []string `json:"keys,omitempty"`
	Track      string   `json:"track,omitempty"`
	Evidence   string   `json:"evidence,omitempty"`
	// Create names the Movie, Series, Artist, Author or Audiobook to
	// create when none exists.
	Create *ItemCreate `json:"create,omitempty"`
}

// ItemCreate is a Movie, Series, Artist, Author or Audiobook the scan
// applier creates under import-worker, named by pkg/names as the import
// names it: a movie by its TMDB id, a series by its TheTVDB id, and -- the
// 2026-11-10 non-video libraries design's D16, a root scan creating the
// containers it finds -- an artist by its MusicBrainz id, an author by its
// Open Library id and an audiobook by its ASIN. The applier pins a
// container's spec.folder to Folder, the on-disk folder the scan resolved
// (an artist's or author's first segment under the root, an audiobook's
// "<author>/<title>"), as a scanned Series' is.
type ItemCreate struct {
	// Kind is "movie", "series", "artist", "author" or "audiobook".
	Kind              string `json:"kind"`
	Name              string `json:"name"`
	TmdbID            int64  `json:"tmdbID,omitempty"`
	TvdbID            int64  `json:"tvdbID,omitempty"`
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`
	RootFolderRef     string `json:"rootFolderRef,omitempty"`
	Title             string `json:"title,omitempty"`
	Year              int32  `json:"year,omitempty"`
	OriginalLanguage  string `json:"originalLanguage,omitempty"`
	// MusicBrainzID is an artist's MusicBrainz artist id, OpenLibraryID an
	// author's Open Library author id ("OL…A"), ASIN an audiobook's Audible
	// ASIN and Region the Audible region the ASIN was resolved in (the
	// root's defaults.region); each is the id of one kind and empty for the
	// others.
	MusicBrainzID string `json:"musicBrainzID,omitempty"`
	OpenLibraryID string `json:"openLibraryID,omitempty"`
	ASIN          string `json:"asin,omitempty"`
	Region        string `json:"region,omitempty"`
	// Folder is the item's folder as the scan found it, relative to the
	// root, which the applier pins into the container's spec.folder so its
	// status.path is the folder its files are already in.
	Folder string `json:"folder,omitempty"`
}

// FrozenFields are the MediaFile spec fields frozen at import (loop spec
// §2.11.2), as an agent computed them.
type FrozenFields struct {
	Quality        *commonv1.Quality    `json:"quality,omitempty"`
	Revision       *commonv1.Revision   `json:"revision,omitempty"`
	ReleaseType    commonv1.ReleaseType `json:"releaseType,omitempty"`
	ReleaseGroup   string               `json:"releaseGroup,omitempty"`
	Edition        *string              `json:"edition,omitempty"`
	Languages      []string             `json:"languages,omitempty"`
	FormatScore    *int32               `json:"formatScore,omitempty"`
	MatchedFormats []string             `json:"matchedFormats,omitempty"`
	ProfileHash    string               `json:"profileHash,omitempty"`
	Original       *bool                `json:"original,omitempty"`
	Track          string               `json:"track,omitempty"`
	// PartIndex orders an audiobook's parts (MediaFileSpec.PartIndex,
	// 2026-11-10 design D11); 0 for a file that is not a part.
	PartIndex int32 `json:"partIndex,omitempty"`
}

// MediaFileBasis is the MediaFile an agent's observation or plan was made
// against: the manager refuses a write whose basis moved.
type MediaFileBasis struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Path            string `json:"path,omitempty"`
	// SpecHash hashes the MediaFile's import-worker spec.
	SpecHash string `json:"specHash,omitempty"`
}

// UnmatchedFact is why a scanned file was not attributed.
type UnmatchedFact struct {
	Code       string   `json:"code"`
	Reason     string   `json:"reason,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
	// Folder marks a folder the scan could not identify as a container
	// (catalogv1alpha1.UnmatchedFile.Folder; the 2026-11-10 non-video
	// libraries design's D16): the observation's Path is the folder, its
	// Candidates the provider's hits.
	Folder bool `json:"folder,omitempty"`
}

// MissingFact is a MediaFile whose file the scan did not find.
type MissingFact struct {
	// ENOENT is true when the stat said the file does not exist, not that
	// it could not be read.
	ENOENT bool      `json:"enoent,omitempty"`
	At     time.Time `json:"at"`
}
