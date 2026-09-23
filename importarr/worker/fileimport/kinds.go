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
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// What this file knows about non-video files, shared with
// importarr/worker/rescan so the importer and the scanner agree on what a
// music, book, audiobook or comic file is and on what quality one is frozen
// with. Which extensions each kind's files have, and which sample and extras
// rules apply to them, is pkg/fsops' (fsops.MediaExtensions, fsops.IsSample,
// fsops.IsExtra); [ClassifierFor] only maps a catalog kind onto an
// fsops.Kind.

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
//     Lossy files are NOT mapped by extension: their tier is a bitrate
//     band (MP3-192 is Low, MP3-256 Mid, MP3-320 High, as in Lidarr), and
//     the bitrate is the file's, which only a probe reads --
//     [FrozenFileQuality] does, and this table is its fallback. .m4a is
//     ALAC or AAC; unknown without a probe.
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
	return ProfileKindFor(kind) != "video"
}

// ClassifierFor is the fsops.Classifier for files of fileKind found under
// root: its kind is fileKind's ladder ([ProfileKindFor]) -- so an album's
// files are music, and a movie's, or those of any kind that is not one of
// the four non-video kinds, are video -- bounded by root, with
// sampleMaxBytes as the video size floor (0 disables it; the non-video kinds
// have none). Both workers and the rescan classify through it, so the
// importer and the scanner agree on what every file is.
func ClassifierFor(fileKind commonv1.MediaKind, root string, sampleMaxBytes int64) fsops.Classifier {
	return fsops.Classifier{
		Kind:           fsops.Kind(ProfileKindFor(fileKind)),
		Root:           root,
		SampleMaxBytes: sampleMaxBytes,
	}
}

// SuspectedSampleReason is the sentence the scanner and the importer both
// record for an fsops.ClassSuspectedSample file of size bytes found under a
// maxBytes threshold: what was suspected, and why that is a question rather
// than a verdict. Each caller appends the remedy its own surface offers. It
// is a sentence for LibraryScan.status.unmatched or Download.status.import,
// never a metric label.
func SuspectedSampleReason(size, maxBytes int64) string {
	return fmt.Sprintf("suspected sample: at %s it is under the %s sample-size threshold, though its name "+
		"does not mark it a sample -- a size alone cannot tell a promo clip from a short film or an old "+
		"low-resolution episode", formatMiB(size), formatMiB(maxBytes))
}

// formatMiB renders n bytes for a person: MiB to one decimal, and the exact
// byte count the threshold is configured in.
func formatMiB(n int64) string {
	return fmt.Sprintf("%.1f MiB (%d bytes)", float64(n)/(1<<20), n)
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

// AudioProber reads the codec, stream bitrate and sample size of an audio
// file: mediainfo.ProbeAudio in production, a stub in a test.
type AudioProber func(ctx context.Context, path string) (mediainfo.AudioProbe, error)

// FrozenFileQuality is the quality a non-video file of fileKind is frozen
// with, reading a music file itself when probe is non-nil.
//
// A music file's tier is its codec and bitrate -- Lidarr's
// QualityParser.FindQuality, which pkg/release.AudioFileQuality ports --
// and neither is in its name: a probe settles a lossy file (MP3-320 is
// High), and a lossless one's sample size (a 24-bit FLAC is "24bit
// Lossless" whatever its name says). The probe's answer is taken only where
// it names a tier of pkg/quality's music ladder; a probe that fails, or a
// codec Lidarr calls "Unknown" (a VBR MP3, whose average bitrate is no
// Lidarr value), falls back to [FrozenQuality]'s extension rule -- and so,
// for a lossy file, to unknown, which only a manual import accepts.
//
// Every other kind is [FrozenQuality]: an audiobook's ladder is per
// format, and a book's or an issue's is its extension.
func FrozenFileQuality(
	ctx context.Context, probe AudioProber, fileKind commonv1.MediaKind, path string, declared ...string,
) (commonv1.Quality, bool) {
	if fileKind == commonv1.MediaKindAlbum && probe != nil {
		if ap, err := probe(ctx, path); err == nil {
			name := release.AudioFileQuality(ap.Codec, ap.BitrateKbps, ap.SampleBits).Name
			if def, ok := quality.Lookup(ProfileKindFor(fileKind), name); ok {
				return def.Quality, true
			}
		}
	}
	return FrozenQuality(fileKind, path, declared...)
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
