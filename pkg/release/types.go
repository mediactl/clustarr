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

package release

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Options configures Parse and ParsePath.
type Options struct {
	// Kind pins the media kind, skipping ClassifyKind. The zero value ("")
	// means auto-detect via ClassifyKind.
	Kind commonv1.MediaKind
	// SeriesType selects the Sonarr-style episode-regex family for TV
	// titles: "standard" (default when empty), "daily" or "anime". Ignored
	// for every other Kind.
	SeriesType string
}

// Hints carries custom-format-only tag detail moistari/rls extracts from a
// title: codec, HDR flags, audio codec/layout, streaming service and
// container. None of these are quality identity (pkg/quality/catalogue
// conditions consume them, not pkg/decision's quality ranking).
type Hints struct {
	Codec     []string // x264, x265, h264, h265, AV1, XviD, VC1, MPEG2
	HDR       []string // HDR10, HDR10+, DV, HLG, PQ, SDR
	Audio     []string // DTS-X, DTS-HD MA, TrueHD, Atmos, EAC3, AC3, AAC, FLAC, Opus, MP3
	Channels  string   // "7.1", "5.1", "2.0"; "" when not present
	Streaming []string // AMZN, NF, DSNP, ATVP, HULU, HMAX, ...
	Container string   // mkv, mp4, avi; "" when not present
}

// MusicInfo is populated when ParsedRelease.ReleaseType == common.ReleaseTypeAlbum.
type MusicInfo struct {
	Artist      string
	Album       string
	Year        int
	Codec       string // MP3, FLAC, AAC, Vorbis, ALAC, WavPack, WAV
	BitrateKbps int32  // 0 when lossless or unknown
	VBR         bool
	SampleBits  int32 // 16 or 24; 0 when unknown/lossy
}

// BookInfo is populated for common.MediaKindBook and common.MediaKindAudiobook.
// Audiobook vs. ebook is a Format value, mirroring Readarr's Edition.IsEbook
// being a property rather than a separate type (docs/research/naming.md §12).
type BookInfo struct {
	Author     string
	Title      string
	Year       int
	Format     string // PDF, MOBI, EPUB, AZW3 (ebook); M4B, MP3, FLAC (audiobook)
	Narrator   string // audiobook only; "" otherwise
	ASIN       string // audiobook only; "" otherwise
	Unabridged bool
}

// ComicInfo is populated for common.MediaKindComic and common.MediaKindIssue.
type ComicInfo struct {
	Series string
	Issue  string // "001", "12.5" (manga chapter), "Annual 1"
	Volume int    // 0 when not a volume-numbered manga release
	Year   int
	Format string // CBZ, CBR, PDF
	Manga  bool
}

// TitleCandidate is one inventory item MatchTitle scores a ParsedRelease against.
type TitleCandidate struct {
	Title string
	Year  int
}

// ParsedRelease is the result of parsing a release title. Field set is
// spec §7's pkg/release block plus LanguageUnknown, which Radarr models as a
// Language.Unknown entry and this package as a flag beside the default.
type ParsedRelease struct {
	Title     string
	Titles    []string
	Year      int
	Quality   commonv1.Quality
	Revision  commonv1.Revision
	Languages []string
	// LanguageUnknown is true when the title names no language: Radarr's
	// and Sonarr's Language.Unknown. Languages then holds this package's
	// item-independent default -- ["English"] for a movie or TV title, nil
	// for anime and every non-video kind -- and LanguagesFor gives the
	// languages for a known item.
	LanguageUnknown bool
	Group           string
	Hash            string // scene obfuscation hash token, e.g. trailing -a1b2c3d4; "" normally
	Edition         string
	Seasons         []int
	Episodes        []int
	Absolute        []int
	AirDate         *time.Time
	FullSeason      bool
	Partial         bool
	MultiSeason     bool
	Special         bool
	ReleaseType     commonv1.ReleaseType
	Music           *MusicInfo
	Book            *BookInfo
	Comic           *ComicInfo
	Hints           Hints
	IDs             map[string]string
}
