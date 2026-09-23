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

package fileimport

import (
	"path/filepath"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/quality"
)

// What this file knows about non-video files, shared with
// importarr/worker/rescan so the importer and the scanner agree on what a
// music, book, audiobook or comic file is and on what quality one is frozen
// with.
//
// fsops.Walk's own classification is video-shaped and cannot be used for
// these kinds as it stands: fsops.MediaExtensions has no audio extension at
// all (every .flac, .mp3 and .m4b classifies as ClassOther), and
// fsops.IsSample flags any media-extension file under 50 MiB as a sample, so
// nearly every ebook and many comics classify as ClassSample. Both are
// pkg/fsops defects this package does not own; [ClassifyFor] is the narrow
// per-kind replacement callers use instead of the class fsops.Walk passes in.

// fileExtensions is, per file-bearing kind, the extensions that kind's files
// have. Each set is grounded in docs/research/naming.md: Lidarr's quality
// list for music (MP3, AAC, Vorbis, FLAC, ALAC, WavPack, APE, WAV, WMA),
// Readarr and Audiobookshelf for audiobooks (MP3, M4B, FLAC; m4a), Readarr
// and Jellyfin for ebooks (EPUB, MOBI, AZW, AZW3, PDF), and Kavita/Jellyfin
// for comics (cbz, cbr, cb7, cbt, pdf).
var fileExtensions = map[commonv1.MediaKind]map[string]bool{
	commonv1.MediaKindAlbum: {
		".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true,
		".wv": true, ".ape": true, ".wav": true, ".wma": true,
	},
	commonv1.MediaKindAudiobook: {".m4b": true, ".m4a": true, ".mp3": true, ".flac": true},
	commonv1.MediaKindBook:      {".epub": true, ".mobi": true, ".azw": true, ".azw3": true, ".pdf": true},
	commonv1.MediaKindIssue:     {".cbz": true, ".cbr": true, ".cb7": true, ".cbt": true, ".pdf": true},
}

// frozenQualityNames maps an extension onto a pkg/quality definition name
// (pkg/quality's nonVideoDefinitions, the ladders the built-in music-*,
// ebook, audiobook and comic profiles are made of). A mapping exists only
// where the extension settles the tier by the *arr ecosystem's own rule;
// everything else stays unknown rather than guessed, because
// MediaFileSpec.Quality is frozen at import and a wrong value is permanent:
//
//   - music: lossless extensions (.flac, .ape, .wv) are the "FLAC" tier --
//     Lidarr's Lossless group is FLAC=ALAC=APE=WavPack
//     (docs/research/quality.md §music) -- promoted to "24bit Lossless" only
//     where a 24-bit marker is declared (see [FrozenQuality]); .wav is WAV.
//     Lossy files are NOT mapped: their tiers are bitrate bands, the
//     bitrate needs a probe, and pkg/quality's collapsed ladder puts
//     "MP3-192" above "Mid" where Lidarr's puts MP3-192 in "Low", so even a
//     declared bitrate has no agreed tier. .m4a is ALAC or AAC; unknown.
//   - audiobook: the ladder is per format; .m4a is an audio format outside
//     it, which is what the ladder's own "Unknown Audio" tier is for.
//   - book: .azw is not AZW3 and has no tier.
//   - comic: .cb7 and .cbt have no tier.
var frozenQualityNames = map[commonv1.MediaKind]map[string]string{
	commonv1.MediaKindAlbum: {".wav": "WAV", ".flac": "FLAC", ".ape": "FLAC", ".wv": "FLAC"},
	commonv1.MediaKindAudiobook: {
		".m4b": "M4B", ".mp3": "MP3", ".flac": "FLAC", ".m4a": "Unknown Audio",
	},
	commonv1.MediaKindBook:  {".epub": "EPUB", ".mobi": "MOBI", ".azw3": "AZW3", ".pdf": "PDF"},
	commonv1.MediaKindIssue: {".cbz": "CBZ", ".cbr": "CBR", ".pdf": "PDF"},
}

// sampleSize24RE is Lidarr's 24-bit sample-size marker ("24bit", "24-bit",
// "24 bit", "24BIT"), as a whole token.
var sampleSize24RE = regexp.MustCompile(`(?i)(?:^|[^0-9a-z])24[ _-]?bit(?:[^a-z]|$)`)

// IsNonVideoFileKind reports whether kind is one of the four non-video kinds
// a file is attributed to: album, book, audiobook, issue.
func IsNonVideoFileKind(kind commonv1.MediaKind) bool {
	_, ok := fileExtensions[kind]
	return ok
}

// ClassifyFor classifies path as a file of fileKind: a partial transfer, an
// extra (by the Jellyfin extras-folder list), a sample by NAME (the size
// heuristic is video-only and deliberately not applied), media when the
// extension belongs to fileKind, and other otherwise. fileKind must satisfy
// [IsNonVideoFileKind]; any other kind classifies everything as other.
func ClassifyFor(fileKind commonv1.MediaKind, path string) fsops.FileClass {
	switch {
	case fsops.IsPart(path):
		return fsops.ClassPart
	case fsops.IsExtra(path):
		return fsops.ClassExtra
	case fsops.IsSample(path, 0): // size 0 disables the video size heuristic
		return fsops.ClassSample
	case fileExtensions[fileKind][strings.ToLower(filepath.Ext(path))]:
		return fsops.ClassMedia
	default:
		return fsops.ClassOther
	}
}

// ProfileKindFor is the QualityProfile mediaKind (and pkg/quality ladder)
// that governs files of fileKind.
func ProfileKindFor(fileKind commonv1.MediaKind) string {
	switch fileKind {
	case commonv1.MediaKindAlbum:
		return "music"
	case commonv1.MediaKindBook:
		return "book"
	case commonv1.MediaKindAudiobook:
		return "audiobook"
	case commonv1.MediaKindIssue:
		return "comic"
	default:
		return "video"
	}
}

// FrozenQuality returns the quality a non-video file of fileKind is frozen
// with, taken verbatim from pkg/quality's definition so it compares equal to
// a profile tier, and false when the extension cannot settle it. declared is
// whatever names the release the file came from -- the Download's release
// title, the file's path under the root -- and is consulted for one thing
// only: a lossless music file is "24bit Lossless" when any of it carries a
// 24-bit marker, and "FLAC" otherwise (Lidarr's own default).
func FrozenQuality(fileKind commonv1.MediaKind, path string, declared ...string) (commonv1.Quality, bool) {
	name, ok := frozenQualityNames[fileKind][strings.ToLower(filepath.Ext(path))]
	if !ok {
		return commonv1.Quality{}, false
	}
	if fileKind == commonv1.MediaKindAlbum && name == "FLAC" {
		for _, d := range declared {
			if sampleSize24RE.MatchString(d) {
				name = "24bit Lossless"
				break
			}
		}
	}
	def, ok := quality.Lookup(ProfileKindFor(fileKind), name)
	if !ok {
		return commonv1.Quality{}, false
	}
	return def.Quality, true
}

// ReleaseYear is the calendar year of a provider release date, read in UTC,
// or 0 for nil. metav1.Time.UnmarshalJSON converts every decoded time into
// the replica's LOCAL zone (MarshalJSON always writes UTC), so on a replica
// west of UTC a release dated 1 January 00:30 UTC reads as 31 December of the
// year before -- a wrong year in a rendered folder name and a failed year
// match during attribution. The same fix as
// catalogarr/controller/audiobook/path.go's namingContext.
func ReleaseYear(t *metav1.Time) int {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.UTC().Year()
}

// ReleaseTypeFor is the release type recorded for one file of fileKind: the
// single-item value of commonv1.ReleaseType for that kind. pkg/release
// reports audiobooks as ReleaseTypeBook too, having no audiobook value.
func ReleaseTypeFor(fileKind commonv1.MediaKind) commonv1.ReleaseType {
	switch fileKind {
	case commonv1.MediaKindAlbum:
		return commonv1.ReleaseTypeAlbum
	case commonv1.MediaKindBook, commonv1.MediaKindAudiobook:
		return commonv1.ReleaseTypeBook
	case commonv1.MediaKindIssue:
		return commonv1.ReleaseTypeIssue
	default:
		return ""
	}
}

// singleFileKind reports whether one catalog item of fileKind is backed by
// one file (a book in one format, an issue) rather than a set of files (an
// album's tracks, an audiobook's parts). Only a single-file item's existing
// file is something a new import can be an upgrade over.
func singleFileKind(fileKind commonv1.MediaKind) bool {
	return fileKind == commonv1.MediaKindBook || fileKind == commonv1.MediaKindIssue
}
