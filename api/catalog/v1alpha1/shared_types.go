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

// ImageType classifies a metadata image.
//
// +kubebuilder:validation:Enum=poster;fanart;logo
type ImageType string

// Image types.
const (
	ImageTypePoster ImageType = "poster"
	ImageTypeFanart ImageType = "fanart"
	ImageTypeLogo   ImageType = "logo"
)

// Image is one artwork URL published by a metadata provider.
type Image struct {
	// Type classifies the image.
	// +required
	Type ImageType `json:"type"`

	// URL is where the image can be fetched.
	// +required
	URL string `json:"url"`
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
	// ReleaseTitle is the raw title of the release that will be grabbed.
	// +required
	ReleaseTitle string `json:"releaseTitle"`

	// Protocol is the transfer protocol of the pending release.
	// +optional
	Protocol commonv1.Protocol `json:"protocol,omitempty"`

	// GrabAt is when the delay expires and the grab will be issued.
	// +required
	GrabAt metav1.Time `json:"grabAt"`
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
