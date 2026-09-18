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

// Source is the origin of a video release.
//
// +kubebuilder:validation:Enum=unknown;cam;telesync;telecine;workprint;dvd;tv;webdl;webrip;bluray
type Source string

// Video sources.
const (
	SourceUnknown   Source = "unknown"
	SourceCam       Source = "cam"
	SourceTelesync  Source = "telesync"
	SourceTelecine  Source = "telecine"
	SourceWorkprint Source = "workprint"
	SourceDVD       Source = "dvd"
	SourceTV        Source = "tv"
	SourceWebDL     Source = "webdl"
	SourceWebRip    Source = "webrip"
	SourceBluray    Source = "bluray"
)

// Modifier refines a video quality beyond its source and resolution.
//
// +kubebuilder:validation:Enum=none;regional;screener;rawhd;brdisk;remux
type Modifier string

// Quality modifiers.
const (
	ModifierNone     Modifier = "none"
	ModifierRegional Modifier = "regional"
	ModifierScreener Modifier = "screener"
	ModifierRawHD    Modifier = "rawhd"
	ModifierBRDisk   Modifier = "brdisk"
	ModifierRemux    Modifier = "remux"
)

// Video resolutions accepted by Quality.Resolution. Zero means unknown or
// not applicable (music, books, audiobooks, comics).
const (
	ResolutionUnknown int32 = 0
	Resolution360p    int32 = 360
	Resolution480p    int32 = 480
	Resolution540p    int32 = 540
	Resolution576p    int32 = 576
	Resolution720p    int32 = 720
	Resolution1080p   int32 = 1080
	Resolution2160p   int32 = 2160
)

// Quality identifies a quality definition. For video the name is derived from
// the (source, resolution, modifier) tuple; for music, books, audiobooks and
// comics it is a table name such as FLAC, MP3-320, EPUB, M4B or CBZ.
type Quality struct {
	// Name is the canonical quality definition name.
	// +required
	Name string `json:"name"`

	// Source is the release source. Video only.
	// +optional
	Source Source `json:"source,omitempty"`

	// Resolution is the vertical resolution in lines. Video only.
	// +optional
	// +kubebuilder:validation:Enum=0;360;480;540;576;720;1080;2160
	Resolution int32 `json:"resolution,omitempty"`

	// Modifier refines the quality (remux, screener, ...).
	// +optional
	Modifier Modifier `json:"modifier,omitempty"`
}

// Revision records the proper/repack revision of a release.
type Revision struct {
	// Version is the release version; 1 is the original, 2+ are propers.
	// +optional
	// +kubebuilder:default=1
	Version int32 `json:"version"`

	// Real counts REAL re-releases that fix a broken proper.
	// +optional
	// +kubebuilder:default=0
	Real int32 `json:"real"`

	// Repack is true when the release is flagged as a repack.
	// +optional
	// +kubebuilder:default=false
	Repack bool `json:"repack"`
}
