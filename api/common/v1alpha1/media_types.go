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

// MediaKind identifies what kind of catalog item a reference points at.
//
// +kubebuilder:validation:Enum=movie;series;episode;artist;album;author;book;audiobook;comic;issue
type MediaKind string

// Media kinds.
const (
	MediaKindMovie     MediaKind = "movie"
	MediaKindSeries    MediaKind = "series"
	MediaKindEpisode   MediaKind = "episode"
	MediaKindArtist    MediaKind = "artist"
	MediaKindAlbum     MediaKind = "album"
	MediaKindAuthor    MediaKind = "author"
	MediaKindBook      MediaKind = "book"
	MediaKindAudiobook MediaKind = "audiobook"
	MediaKindComic     MediaKind = "comic"
	MediaKindIssue     MediaKind = "issue"
)

// MediaRef points at a catalog object of the given kind in the same namespace
// as the referencing object.
type MediaRef struct {
	// Kind is the media kind of the referenced object.
	// +required
	Kind MediaKind `json:"kind"`

	// Name is the name of the referenced object; it lives in the same
	// namespace as the object holding the reference.
	// +required
	Name string `json:"name"`

	// Keys lists the Episode or Issue names covered by a pack release.
	// +optional
	// +kubebuilder:validation:MaxItems=200
	Keys []string `json:"keys,omitempty"`
}

// HdrFormat is the high-dynamic-range format of a video stream, using the
// *arr vocabulary.
//
// +kubebuilder:validation:Enum=none;pq10;hdr10;hdr10plus;hlg10;dolbyVision;dolbyVisionHdr10;dolbyVisionSdr;dolbyVisionHlg;dolbyVisionHdr10Plus
type HdrFormat string

// HDR formats.
const (
	HdrFormatNone                 HdrFormat = "none"
	HdrFormatPQ10                 HdrFormat = "pq10"
	HdrFormatHDR10                HdrFormat = "hdr10"
	HdrFormatHDR10Plus            HdrFormat = "hdr10plus"
	HdrFormatHLG10                HdrFormat = "hlg10"
	HdrFormatDolbyVision          HdrFormat = "dolbyVision"
	HdrFormatDolbyVisionHDR10     HdrFormat = "dolbyVisionHdr10"
	HdrFormatDolbyVisionSDR       HdrFormat = "dolbyVisionSdr"
	HdrFormatDolbyVisionHLG       HdrFormat = "dolbyVisionHlg"
	HdrFormatDolbyVisionHDR10Plus HdrFormat = "dolbyVisionHdr10Plus"
)

// AudioStream describes one audio track of a media file.
type AudioStream struct {
	// Index is the zero-based stream index inside the container.
	// +optional
	Index int32 `json:"index,omitempty"`

	// Codec is the audio codec name, e.g. aac, eac3, truehd, flac.
	// +optional
	Codec string `json:"codec,omitempty"`

	// Profile is the codec profile, if any, e.g. Atmos or DTS-HD MA.
	// +optional
	Profile string `json:"profile,omitempty"`

	// Language is the ISO 639 language tag of the track.
	// +optional
	Language string `json:"language,omitempty"`

	// Title is the track title from the container metadata.
	// +optional
	Title string `json:"title,omitempty"`

	// Channels is the channel count (2 = stereo, 6 = 5.1, 8 = 7.1).
	// +optional
	Channels int32 `json:"channels,omitempty"`

	// BitrateKbps is the track bitrate in kilobits per second.
	// +optional
	BitrateKbps int32 `json:"bitrateKbps,omitempty"`

	// Default is true when the container flags this as the default audio track.
	// +optional
	Default bool `json:"default,omitempty"`

	// Commentary is true when the track is a commentary track.
	// +optional
	Commentary bool `json:"commentary,omitempty"`
}

// SubtitleStream describes one subtitle track of a media file.
type SubtitleStream struct {
	// Index is the zero-based stream index inside the container.
	// +optional
	Index int32 `json:"index,omitempty"`

	// Codec is the subtitle codec name, e.g. subrip, ass, hdmv_pgs_subtitle.
	// +optional
	Codec string `json:"codec,omitempty"`

	// Language is the ISO 639 language tag of the track.
	// +optional
	Language string `json:"language,omitempty"`

	// Title is the track title from the container metadata.
	// +optional
	Title string `json:"title,omitempty"`

	// Forced is true when the track is flagged as forced.
	// +optional
	Forced bool `json:"forced,omitempty"`

	// HearingImpaired is true when the track is flagged as SDH / hearing impaired.
	// +optional
	HearingImpaired bool `json:"hearingImpaired,omitempty"`

	// Bitmap is true for image-based subtitles (PGS, VobSub) as opposed to text.
	// +optional
	Bitmap bool `json:"bitmap,omitempty"`
}

// MediaInfo is the technical description of a media file, following the *arr
// MediaInfoModel vocabulary.
type MediaInfo struct {
	// Container is the container format, e.g. mkv, mp4, flac, epub.
	// +optional
	Container string `json:"container,omitempty"`

	// VideoCodec is the video codec name, e.g. h264, hevc, av1.
	// +optional
	VideoCodec string `json:"videoCodec,omitempty"`

	// VideoProfile is the video codec profile, e.g. Main 10.
	// +optional
	VideoProfile string `json:"videoProfile,omitempty"`

	// PixelFormat is the pixel format, e.g. yuv420p10le.
	// +optional
	PixelFormat string `json:"pixelFormat,omitempty"`

	// VideoBitDepth is the video bit depth (8, 10, 12).
	// +optional
	VideoBitDepth int32 `json:"videoBitDepth,omitempty"`

	// Width is the video frame width in pixels.
	// +optional
	Width int32 `json:"width,omitempty"`

	// Height is the video frame height in pixels.
	// +optional
	Height int32 `json:"height,omitempty"`

	// FpsMilli is the video frame rate in thousandths of a frame per second,
	// so 23.976 fps is 23976. Scaled integers are used instead of floats because
	// CRD schemas discourage floating-point values.
	// +optional
	// +kubebuilder:validation:Minimum=0
	FpsMilli int32 `json:"fpsMilli,omitempty"`

	// VideoBitrateKbps is the video bitrate in kilobits per second.
	// +optional
	VideoBitrateKbps int32 `json:"videoBitrateKbps,omitempty"`

	// Hdr is the HDR format of the video stream.
	// +optional
	Hdr HdrFormat `json:"hdr,omitempty"`

	// DoviProfile is the Dolby Vision profile number, when present.
	// +optional
	DoviProfile *int32 `json:"doviProfile,omitempty"`

	// DoviBLCompatID is the Dolby Vision base-layer compatibility ID, when present.
	// +optional
	DoviBLCompatID *int32 `json:"doviBLCompatID,omitempty"`

	// RuntimeMillis is the total duration of the file in milliseconds.
	// +optional
	// +kubebuilder:validation:Minimum=0
	RuntimeMillis int64 `json:"runtimeMillis,omitempty"`

	// Audio lists the audio tracks.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Audio []AudioStream `json:"audio,omitempty"`

	// Subtitles lists the embedded subtitle tracks.
	// +optional
	// +kubebuilder:validation:MaxItems=64
	Subtitles []SubtitleStream `json:"subtitles,omitempty"`

	// Attachments is the number of attachment streams (fonts, cover art).
	// +optional
	Attachments int32 `json:"attachments,omitempty"`

	// Chapters is the number of chapters.
	// +optional
	Chapters int32 `json:"chapters,omitempty"`
}
