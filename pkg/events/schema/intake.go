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
// manager's catalogarr-intake-candidate (ADR-0019 §4.3, §6.6): the RSS
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
// importarr-intake-scan (ADR-0019 §7.6). The manager's scan applier
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
	// Create names the Movie or Series to create when none exists.
	Create *ItemCreate `json:"create,omitempty"`
}

// ItemCreate is a Movie or Series the scan applier creates under
// importarr-worker, named by names.Movie or names.Series.
type ItemCreate struct {
	// Kind is "movie" or "series".
	Kind              string `json:"kind"`
	Name              string `json:"name"`
	TmdbID            int64  `json:"tmdbID,omitempty"`
	TvdbID            int64  `json:"tvdbID,omitempty"`
	QualityProfileRef string `json:"qualityProfileRef,omitempty"`
	RootFolderRef     string `json:"rootFolderRef,omitempty"`
	Title             string `json:"title,omitempty"`
	Year              int32  `json:"year,omitempty"`
	OriginalLanguage  string `json:"originalLanguage,omitempty"`
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
}

// MediaFileBasis is the MediaFile an agent's observation or plan was made
// against: the manager refuses a write whose basis moved.
type MediaFileBasis struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Path            string `json:"path,omitempty"`
	// SpecHash hashes the MediaFile's importarr-worker spec.
	SpecHash string `json:"specHash,omitempty"`
}

// UnmatchedFact is why a scanned file was not attributed.
type UnmatchedFact struct {
	Code       string   `json:"code"`
	Reason     string   `json:"reason,omitempty"`
	Candidates []string `json:"candidates,omitempty"`
}

// MissingFact is a MediaFile whose file the scan did not find.
type MissingFact struct {
	// ENOENT is true when the stat said the file does not exist, not that
	// it could not be read.
	ENOENT bool      `json:"enoent,omitempty"`
	At     time.Time `json:"at"`
}
