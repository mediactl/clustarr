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

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Label keys the catalogarr controllers mirror onto catalog objects and the
// MediaFile backing them, so that files can be selected by technical shape.
const (
	// LabelKind carries the common.MediaKind of the object a MediaFile backs.
	LabelKind = "catalog.clustarr.io/kind"
	// LabelResolution carries the vertical resolution of the imported file.
	LabelResolution = "catalog.clustarr.io/resolution"
	// LabelSource carries the release source of the imported file.
	LabelSource = "catalog.clustarr.io/source"
	// LabelModifier carries the quality modifier of the imported file.
	LabelModifier = "catalog.clustarr.io/modifier"
	// LabelVideoCodec carries the video codec of the imported file.
	LabelVideoCodec = "catalog.clustarr.io/video-codec"
	// LabelHdr carries the HDR format of the imported file.
	LabelHdr = "catalog.clustarr.io/hdr"
	// LabelOriginal is "true" while a MediaFile is the originally imported file.
	LabelOriginal = "catalog.clustarr.io/original"
)

// AnnotationSearchNow triggers an immediate search for a catalog item when set
// to "now".
const AnnotationSearchNow = "catalog.clustarr.io/search"

// ImageType classifies a metadata image. The values are exactly
// pkg/metadata.ImageType's nine roles, so a provider image never has to be
// dropped for want of a CRD token.
//
// +kubebuilder:validation:Enum=poster;fanart;banner;logo;clearart;thumb;screenshot;disc;headshot
type ImageType string

// AnnotationRefreshMetadata is the operator's forced metadata refresh on a
// kind with metadata of its own (Movie, Series, Artist, Album, Author, Book,
// Audiobook, Comic): its value is a positive integer, by convention the
// requester's Unix time. catalogarr's refresher publishes a MetadataTask
// carrying it as the refresh epoch (spec §5) and consumes the annotation;
// the UI's "Refresh metadata" writes it. Declared here because both the
// writer (ui/actions) and the consumer (app/catalog/metadata) read it.
const AnnotationRefreshMetadata = "clustarr.io/refresh-metadata"

// Image types.
const (
	ImageTypePoster     ImageType = "poster"
	ImageTypeFanart     ImageType = "fanart"
	ImageTypeBanner     ImageType = "banner"
	ImageTypeLogo       ImageType = "logo"
	ImageTypeClearart   ImageType = "clearart"
	ImageTypeThumb      ImageType = "thumb"
	ImageTypeScreenshot ImageType = "screenshot"
	ImageTypeDisc       ImageType = "disc"
	ImageTypeHeadshot   ImageType = "headshot"
)

// AudioPolicy is a profile's wanted audio languages (anime dual-audio spec
// §5.1).
// +kubebuilder:validation:XValidation:rule="!has(self.default) || self.default in self.languages",message="audio.default must be one of audio.languages"
type AudioPolicy struct {
	// Languages are BCP-47 tags, or "original" for the item's own
	// original language. A file is complete when it carries every one.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=4
	// +listType=set
	Languages []string `json:"languages"`

	// Graft lets a release missing some of Languages be completed with
	// another release's audio (spec §6, §7).
	// +optional
	Graft bool `json:"graft,omitempty"`

	// Default is the language marked default in a grafted file, one of
	// Languages; unset, the default flag stays where the file had it.
	// +optional
	Default string `json:"default,omitempty"`
}

// AudioState is what a file's audio carries against its profile's
// AudioPolicy (anime dual-audio spec §5.3).
type AudioState struct {
	// Wanted are the profile's languages, resolved to tags.
	// +optional
	// +kubebuilder:validation:MaxItems=4
	Wanted []string `json:"wanted,omitempty"`

	// Present are the file's probed audio languages; empty when unknown.
	// Capped as MediaInfo.Audio is: a MULTi release carries 15-20 dubs.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Present []string `json:"present,omitempty"`

	// Missing are Wanted less Present; empty when Present is unknown.
	// +optional
	// +kubebuilder:validation:MaxItems=4
	Missing []string `json:"missing,omitempty"`

	// Graft is the graft's state.
	// +optional
	// +kubebuilder:validation:Enum=none;searching;grabbed;pending;aligned;failed;done
	Graft string `json:"graft,omitempty"`

	// Reason says why, when Graft is failed.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Reason string `json:"reason,omitempty"`

	// Donor is the newest audio donor imported for this item: from the
	// item's Download with spec.purpose audioDonor whose import finished
	// (status.import.imported[].destPath, spec.release.title,
	// status.import.importedAt), carried forward from this status once
	// seen, so deleting the Download loses nothing (loop spec §2.6). A
	// MediaFile is named per file path, so an upgrade under a new filename
	// would lose it if it lived on the file.
	// +optional
	Donor *AudioDonor `json:"donor,omitempty"`

	// RejectedReleases are donor releases a graft of this item failed with
	// (status.graft.donorFault on its file); the donor search never takes
	// them again. Oldest dropped past MaxRejectedReleases.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=512
	RejectedReleases []string `json:"rejectedReleases,omitempty"`
}

// AudioDonor is the reduced donor audio an item's graft takes its missing
// languages from (loop spec §2.6).
type AudioDonor struct {
	// Path is the reduced donor audio (<stem>.mka), which fileimport writes
	// at import.
	// +kubebuilder:validation:MaxLength=4096
	Path string `json:"path"`
	// Release is the donor Download's release title.
	// +kubebuilder:validation:MaxLength=512
	Release string `json:"release"`
	// DownloadRef names the donor Download.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	DownloadRef string `json:"downloadRef,omitempty"`
	// ImportedAt is when the donor's import finished.
	ImportedAt metav1.Time `json:"importedAt"`
}

// Bounds of AudioState that its renderer (rollup.AudioStateAC) holds to,
// mirrored by pkg/crdcheck.TestAudioStateBoundsMatchTheCRD. The donor's path
// and release take MaxPathLength and MaxReleaseTitleLength.
const (
	MaxRejectedReleases  = 16
	MaxAudioReasonLength = 1024
)

// Image is one artwork URL published by a metadata provider.
type Image struct {
	// Type classifies the image.
	// +required
	Type ImageType `json:"type"`

	// URL is where the image can be fetched.
	// +required
	URL string `json:"url"`

	// Language is the ISO 639-1 language of any text in the image, empty for
	// an image with none. The metadata gateway keeps the configured
	// language's images and the original language's side by side, so the
	// Plex provider can answer OriginalImage (spec 2026-09-30
	// plex-full-metadata-response §3.2).
	// +optional
	Language string `json:"language,omitempty"`
}

// Certification is one country's age rating for an item.
type Certification struct {
	// Country is the ISO 3166-1 alpha-2 code the rating applies in.
	// +required
	Country string `json:"country"`

	// Rating is the certification as that country writes it ("R", "15",
	// "FSK 12").
	// +required
	Rating string `json:"rating"`
}

// SeasonImage is one piece of artwork for a single season of a series.
type SeasonImage struct {
	// Season is the season number the image belongs to.
	// +required
	Season int32 `json:"season"`

	// Type classifies the image.
	// +required
	Type ImageType `json:"type"`

	// URL is where the image can be fetched.
	// +required
	URL string `json:"url"`

	// Order is the episode order the season is numbered in (TheTVDB's
	// season type: official, dvd or absolute); empty is official. The Plex
	// provider shows the posters of the series' own order.
	// +optional
	// +kubebuilder:validation:MaxLength=32
	Order string `json:"order,omitempty"`
}

// PlexSeasonRef is one season's id in Plex's own metadata service, which
// the ui's Plex provider answers the season with as plex://season/<id>.
type PlexSeasonRef struct {
	// Number is the season number.
	// +required
	Number int32 `json:"number"`

	// ID is Plex's 24-hex id, without the plex://season/ prefix.
	// +required
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{24}$`
	ID string `json:"id"`
}

// SeasonTypeRef names one of a series' episode orderings as the provider
// calls it (TVDB seasonTypes: "official" is "Aired Order").
type SeasonTypeRef struct {
	// ID is the ordering's identifier, e.g. "official", "dvd", "absolute".
	// +required
	ID string `json:"id"`

	// Name is the ordering's human-readable name.
	// +required
	Name string `json:"name"`
}

// MinimumAvailability is the point in a release cycle at which an item becomes
// eligible for automatic searching.
//
// +kubebuilder:validation:Enum=tba;announced;inCinemas;released
type MinimumAvailability string

// Minimum availability values.
const (
	MinimumAvailabilityTBA       MinimumAvailability = "tba"
	MinimumAvailabilityAnnounced MinimumAvailability = "announced"
	MinimumAvailabilityInCinemas MinimumAvailability = "inCinemas"
	MinimumAvailabilityReleased  MinimumAvailability = "released"
)

// SeriesType selects the numbering and naming dialect used for a series.
//
// +kubebuilder:validation:Enum=standard;daily;anime
type SeriesType string

// Series types.
const (
	SeriesTypeStandard SeriesType = "standard"
	SeriesTypeDaily    SeriesType = "daily"
	SeriesTypeAnime    SeriesType = "anime"
)

// MonitorNewItemsMode says what happens to items a metadata refresh discovers
// after the parent was added.
//
// +kubebuilder:validation:Enum=all;none;new
type MonitorNewItemsMode string

// Monitor-new-items modes.
const (
	MonitorNewItemsAll  MonitorNewItemsMode = "all"
	MonitorNewItemsNone MonitorNewItemsMode = "none"
	MonitorNewItemsNew  MonitorNewItemsMode = "new"
)

// MonitorNewChildrenMode is the two-valued variant of MonitorNewItemsMode used
// where "new" would be meaningless.
//
// +kubebuilder:validation:Enum=all;none
type MonitorNewChildrenMode string

// Monitor-new-children modes.
const (
	MonitorNewChildrenAll  MonitorNewChildrenMode = "all"
	MonitorNewChildrenNone MonitorNewChildrenMode = "none"
)

// PendingGrab records a release that has been chosen but is waiting out a
// DelayProfile delay before it is grabbed.
type PendingGrab struct {
	// ReleaseTitle is the raw title of the release that will be grabbed,
	// clamped on a rune boundary (a stored longer value stays valid under
	// validation ratcheting).
	// +required
	// +kubebuilder:validation:MaxLength=512
	ReleaseTitle string `json:"releaseTitle"`

	// Protocol is the transfer protocol of the pending release.
	// +optional
	Protocol commonv1.Protocol `json:"protocol,omitempty"`

	// GrabAt is when the delay expires and the grab will be issued.
	// +required
	GrabAt metav1.Time `json:"grabAt"`

	// Candidate is the release this pending grab will be made from
	// (ADR-0019 §6.2), so the grab needs nothing but status.
	// +optional
	Candidate *GrabCandidate `json:"candidate,omitempty"`

	// Episodes and Issues name what a Series' or Comic's pending grab covers.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	Episodes []EpisodeNumber `json:"episodes,omitempty"`
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=200
	// +kubebuilder:validation:items:MaxLength=16
	Issues []string `json:"issues,omitempty"`
}

// NamedRef is a display name paired with the provider ID it resolved from.
type NamedRef struct {
	// Name is the display name.
	// +required
	Name string `json:"name"`

	// ASIN is the Audible ASIN of the named entity, when known.
	// +optional
	ASIN string `json:"asin,omitempty"`
}

// ArtworkSource records whether an ArtworkEntry was fetched from the
// metadata provider or from a spec.artwork override.
//
// +kubebuilder:validation:Enum=provider;custom
type ArtworkSource string

// Artwork sources.
const (
	ArtworkSourceProvider ArtworkSource = "provider"
	ArtworkSourceCustom   ArtworkSource = "custom"
)

// ArtworkOverride pins a custom URL for one image type, replacing whatever
// the metadata provider published for it. R3: a URL that fails to fetch
// never falls back to the provider image -- the entry and object stay as
// they were, and an Event says why.
type ArtworkOverride struct {
	// Type classifies the image being overridden.
	// +required
	Type ImageType `json:"type"`

	// URL is where the replacement image is fetched from.
	// +required
	// +kubebuilder:validation:Pattern=`^https?://`
	// +kubebuilder:validation:MaxLength=2048
	URL string `json:"url"`
}

// ArtworkEntry records one artwork image fetched into the artwork store,
// whether from the provider or from a spec.artwork override. Written by the
// metadata gateway under the manager that already writes status.metadata.
type ArtworkEntry struct {
	// Type classifies the image.
	// +required
	Type ImageType `json:"type"`

	// Source is whether this image came from the provider or a spec.artwork
	// override.
	// +required
	Source ArtworkSource `json:"source"`

	// SourceURL is the URL the image was fetched from.
	// +required
	SourceURL string `json:"sourceURL"`

	// Digest is the hex SHA-256 of the stored original image.
	// +required
	Digest string `json:"digest"`

	// SizeBytes is the size in bytes of the stored original image.
	// +required
	SizeBytes int64 `json:"sizeBytes"`

	// UpdatedAt is when the image was last fetched.
	// +required
	UpdatedAt metav1.Time `json:"updatedAt"`
}

// OverlayEntry records the rating-badge overlay rendered onto a Movie or
// Series poster. Written by the renderer under k8s.ManagerCatalogarrArtwork.
type OverlayEntry struct {
	// ProfileRef is the name of the OverlayProfile the overlay was rendered from.
	// +required
	ProfileRef string `json:"profileRef"`

	// Digest is the hex SHA-256 of the rendered overlay image.
	// +required
	Digest string `json:"digest"`

	// RenderedFrom is the digest of the source artwork and ratings the
	// overlay was rendered from, so a later render can tell whether either
	// input has changed.
	// +required
	RenderedFrom string `json:"renderedFrom"`

	// UpdatedAt is when the overlay was last rendered.
	// +required
	UpdatedAt metav1.Time `json:"updatedAt"`
}

// RatingSource is an upstream rating provider.
//
// +kubebuilder:validation:Enum=imdb;tmdb;rottenTomatoesCritic;rottenTomatoesAudience;metacritic;trakt;letterboxd
type RatingSource string

// Rating sources.
const (
	RatingSourceIMDb       RatingSource = "imdb"
	RatingSourceTMDB       RatingSource = "tmdb"
	RatingSourceRTCritic   RatingSource = "rottenTomatoesCritic"
	RatingSourceRTAudience RatingSource = "rottenTomatoesAudience"
	RatingSourceMetacritic RatingSource = "metacritic"
	RatingSourceTrakt      RatingSource = "trakt"
	RatingSourceLetterboxd RatingSource = "letterboxd"
)

// Rating is one score reported by one rating source.
type Rating struct {
	// Source is the rating provider.
	// +required
	Source RatingSource `json:"source"`

	// ValueCentis is the score scaled by 100: 0-1000 for a source out of 10
	// (imdb, tmdb, trakt, letterboxd), 0-10000 for a source out of 100
	// (rottenTomatoesCritic, rottenTomatoesAudience, metacritic). Scaled
	// rather than a float -- CLAUDE.md bans float32/float64 in api/.
	// +required
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10000
	ValueCentis int32 `json:"valueCentis"`

	// Votes is the number of votes behind the score, when the source reports one.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Votes int32 `json:"votes,omitempty"`
}

// SeriesLink places a book or audiobook inside a reading order.
type SeriesLink struct {
	// Series is the name of the series.
	// +required
	Series string `json:"series"`

	// Position is the position within the series, e.g. "3" or "2.5".
	// +optional
	Position string `json:"position,omitempty"`

	// Primary is true for the series a work is chiefly part of.
	// +optional
	Primary bool `json:"primary,omitempty"`
}

// AltTitle is an alternate title for a series, optionally scoped to the scene
// season it applies to.
type AltTitle struct {
	// Title is the alternate title.
	// +required
	Title string `json:"title"`

	// SceneSeason is the scene season the alternate title numbers against.
	// +optional
	SceneSeason *int32 `json:"sceneSeason,omitempty"`
}

// The library delete request (docs/superpowers/specs/
// 2026-09-30-library-delete-design.md). The ui, or kubectl, sets
// AnnotationDelete on a library item; importarr's librarydelete controller
// carries it out and deletes the item, or writes AnnotationDeleteError.
const (
	// AnnotationDelete asks for the item to be deleted: DeleteRecords
	// removes the item and its MediaFile records, DeleteFiles its folder
	// and files on disk as well, permanently.
	AnnotationDelete = "catalog.clustarr.io/delete"
	// AnnotationDeleteAddExclusion, "true", also creates an ImportExclusion
	// for the item before it goes.
	AnnotationDeleteAddExclusion = "catalog.clustarr.io/delete-add-exclusion"
	// AnnotationDeleteError is why a delete was refused or failed; importarr
	// writes it, and a new request clears it.
	AnnotationDeleteError = "catalog.clustarr.io/delete-error"

	// DeleteFiles and DeleteRecords are AnnotationDelete's two values.
	DeleteFiles   = "files"
	DeleteRecords = "records"
)
