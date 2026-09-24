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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReleaseType classifies how many catalog items a release covers.
//
// +kubebuilder:validation:Enum=single;multi;seasonPack;album;book;issue
type ReleaseType string

// Release types.
const (
	ReleaseTypeSingle     ReleaseType = "single"
	ReleaseTypeMulti      ReleaseType = "multi"
	ReleaseTypeSeasonPack ReleaseType = "seasonPack"
	ReleaseTypeAlbum      ReleaseType = "album"
	ReleaseTypeBook       ReleaseType = "book"
	ReleaseTypeIssue      ReleaseType = "issue"
)

// Protocol is the transfer protocol of an indexer, download client or release.
//
// +kubebuilder:validation:Enum=torrent;usenet
type Protocol string

// Transfer protocols.
const (
	ProtocolTorrent Protocol = "torrent"
	ProtocolUsenet  Protocol = "usenet"
)

// Indexer flag values carried in ReleaseInfo.IndexerFlags.
const (
	IndexerFlagFreeleech    = "freeleech"
	IndexerFlagHalfleech    = "halfleech"
	IndexerFlagNeutralleech = "neutralleech"
	IndexerFlagDoubleUpload = "doubleupload"
	IndexerFlagInternal     = "internal"
	IndexerFlagExclusive    = "exclusive"
	IndexerFlagScene        = "scene"
)

// MaxAlsoOn mirrors ReleaseInfo.AlsoOn's +kubebuilder:validation:MaxItems; a
// writer truncates to it rather than have the apiserver reject the object.
const MaxAlsoOn = 20

// Well-known keys of ReleaseInfo.IDs.
const (
	IDKeyTMDB = "tmdb"
	IDKeyIMDB = "imdb"
	IDKeyTVDB = "tvdb"
)

// ReleaseInfo is a snapshot of an indexer release as parsed by the search
// pipeline. It is stored on Download.spec.release and in Search results.
type ReleaseInfo struct {
	// GUID is the indexer-scoped unique identifier of the release.
	// +optional
	GUID string `json:"guid,omitempty"`

	// IndexerRef is the name of the Indexer the release came from.
	// +optional
	IndexerRef string `json:"indexerRef,omitempty"`

	// IndexerName is the display name of the indexer at search time.
	// +optional
	IndexerName string `json:"indexerName,omitempty"`

	// AlsoOn lists the other Indexers, by object name, that offered this same
	// release when a federated search merged duplicates (the design's alsoOn
	// provenance, spec 6.2): IndexerRef is the source the merge kept, and
	// AlsoOn never repeats it. Empty for a release only one indexer offered,
	// and for one read from a single indexer's feed. Capped at MaxAlsoOn; the
	// merge truncates beyond it.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	// +kubebuilder:validation:items:MaxLength=253
	AlsoOn []string `json:"alsoOn,omitempty"`

	// Title is the raw release title as published by the indexer.
	// +optional
	Title string `json:"title,omitempty"`

	// Protocol is the transfer protocol of the release.
	// +optional
	Protocol Protocol `json:"protocol,omitempty"`

	// SizeBytes is the release size in bytes.
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`

	// PublishedAt is when the indexer published the release, or nil when the
	// indexer did not report one.
	//
	// This is a pointer because a value would be unpersistable: omitempty does
	// not fire on a struct, metav1.Time marshals its zero value to null, and
	// controller-gen types a non-pointer metav1.Time as a non-nullable
	// date-time string -- so the apiserver rejected the whole resource for any
	// release whose indexer reported no pubDate, which hard-failed the grab.
	//
	// Absence is also genuinely different from a date, and callers must not
	// paper over it: ranking uses publish age as the usenet tiebreaker, so
	// substituting "now" makes a dateless release sort as brand new and
	// substituting the zero time makes it sort as ancient. Neither is true.
	// +optional
	PublishedAt *metav1.Time `json:"publishedAt,omitempty"`

	// DownloadURL is the .torrent / .nzb download link.
	// +optional
	DownloadURL string `json:"downloadURL,omitempty"`

	// MagnetURL is the magnet link, for torrent releases that expose one.
	// +optional
	MagnetURL string `json:"magnetURL,omitempty"`

	// InfoHash is the torrent info hash, when known.
	// +optional
	InfoHash string `json:"infoHash,omitempty"`

	// InfoURL is the release details page on the indexer.
	// +optional
	InfoURL string `json:"infoURL,omitempty"`

	// Seeders is the seeder count reported by the indexer. Torrent only.
	// +optional
	Seeders *int32 `json:"seeders,omitempty"`

	// Leechers is the leecher count reported by the indexer. Torrent only.
	// +optional
	Leechers *int32 `json:"leechers,omitempty"`

	// IndexerFlags lists indexer-specific flags on the release, each at most
	// once: the cap is the enum's seven values, and the one writer
	// (app/indexer/worker/rss.indexerFlags) deduplicates.
	// +optional
	// +kubebuilder:validation:MaxItems=7
	// +kubebuilder:validation:items:Enum=freeleech;halfleech;neutralleech;doubleupload;internal;exclusive;scene
	IndexerFlags []string `json:"indexerFlags,omitempty"`

	// Categories lists the Newznab/Torznab category IDs of the release. A
	// release sits in a category and its parent, so a handful is normal; the
	// list is indexer-supplied, and app/indexer/worker/rss.ProjectRelease keeps
	// the first 50 rather than let one tracker's output fail a whole Search.
	// +optional
	// +kubebuilder:validation:MaxItems=50
	Categories []int32 `json:"categories,omitempty"`

	// Quality is the quality parsed from the release title.
	// +optional
	Quality Quality `json:"quality,omitempty"`

	// Revision is the proper/repack revision parsed from the release title.
	// +optional
	Revision Revision `json:"revision,omitempty"`

	// ReleaseGroup is the release group parsed from the release title.
	// +optional
	ReleaseGroup string `json:"releaseGroup,omitempty"`

	// Edition is the edition parsed from the release title, e.g. Director's Cut.
	// +optional
	Edition string `json:"edition,omitempty"`

	// Languages lists the languages parsed from the release title. The cap
	// matches MediaFileSpec.Languages, which is this list frozen at import.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Languages []string `json:"languages,omitempty"`

	// ReleaseType classifies how many catalog items the release covers.
	// +optional
	ReleaseType ReleaseType `json:"releaseType,omitempty"`

	// FormatScore is the total custom-format score of the release.
	// +optional
	FormatScore int32 `json:"formatScore,omitempty"`

	// MatchedFormats lists the names of the custom formats that matched. The
	// cap matches MediaFileSpec.MatchedFormats, which is this list frozen at
	// import; pkg/decision truncates to it.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	MatchedFormats []string `json:"matchedFormats,omitempty"`

	// IDs maps external ID providers (tmdb, imdb, tvdb, ...) to the ID the
	// indexer reported for the release.
	// +optional
	IDs map[string]string `json:"ids,omitempty"`
}

// ReleaseDecision is declared here, beside ReleaseInfo, rather than in the
// catalog group that uses it (Search.status.results), and that placement is
// load-bearing. It embeds ReleaseInfo inline exactly as the design writes it
// ({common.ReleaseInfo; Approved, TemporarilyRejected, Rejections, Rank}).
// controller-tools v0.22.0's apply-configuration generator flattens an
// embedded struct and then rewrites the package-relative $refs inside it
// (Quality, Protocol, Revision, ReleaseType) against the EMBEDDING type's
// package (convertRefs in pkg/applyconfiguration/openapi.go). When
// ReleaseDecision lived in the catalog package those refs became
// catalog.v1alpha1.Quality and so on, and the generator panicked with
// "allSchemas schema is missing referenced type" -- which is why Search once
// carried +kubebuilder:ac:generate=false and a hand-written apply
// configuration. Declared in the same package as the type it embeds, the
// rewrite is a no-op and generation works. Moving it back brings the panic
// back; `make generate` is the test. (Kept apart from the doc comment below so
// it stays out of the CRD's descriptions.)

// ReleaseDecision is one search result together with the decision engine's
// verdict on it.
type ReleaseDecision struct {
	// ReleaseInfo is the release as parsed from the indexer response.
	ReleaseInfo `json:",inline"`

	// Approved is true when the release passed every check.
	// +optional
	Approved bool `json:"approved,omitempty"`

	// TemporarilyRejected is true when the release may pass a later run.
	// +optional
	TemporarilyRejected bool `json:"temporarilyRejected,omitempty"`

	// Rejections explains why the release was not approved.
	// +optional
	// +kubebuilder:validation:MaxItems=20
	Rejections []Rejection `json:"rejections,omitempty"`

	// Rank is the release's position in the ranked result set; lower is better.
	// +optional
	Rank int32 `json:"rank,omitempty"`
}
