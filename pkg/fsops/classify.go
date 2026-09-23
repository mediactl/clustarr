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

package fsops

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Kind is the kind of media a file is classified as: it selects which
// extensions are media and which sample rule applies. The values are
// pkg/quality's ladder names (catalogv1alpha1.ProfileMediaKind's enum
// values), the vocabulary a caller converting from a RootFolder or a
// catalog kind already has in hand.
type Kind string

// The kinds Classify understands. Any other Kind classifies every file that
// is not a part or an extra as ClassOther.
const (
	KindVideo     Kind = "video"
	KindMusic     Kind = "music"
	KindAudiobook Kind = "audiobook"
	KindBook      Kind = "book"
	KindComic     Kind = "comic"
)

// MediaExtensions is, per Kind, the lowercase, dotted extensions Classify
// treats as ClassMedia absent a Part/Extra/Sample signal. It is a var so a
// caller can extend it.
//
//   - video: the common containers -- Matroska (every docs/research/
//     naming.md standard-format example), MP4 (Plex's own example there,
//     "...{tmdb-272}.mp4") and its .m4v variant, AVI, QuickTime .mov, WMV,
//     MPEG transport streams (.ts, and Blu-ray's .m2ts), MPEG program
//     streams (.mpg/.mpeg) and WebM.
//   - music: Lidarr's quality list (naming.md, "Lidarr (music)": MP3, AAC,
//     Vorbis, FLAC, ALAC, WavPack, APE, WAV, WMA), as extensions; .m4a is
//     the ALAC/AAC container.
//   - audiobook: Readarr's MP3/M4B/FLAC (naming.md, "Readarr"), plus .m4a,
//     the audio format pkg/quality's audiobook ladder files under "Unknown
//     Audio".
//   - book: Readarr's and Jellyfin's EPUB, MOBI, AZW, AZW3, PDF.
//   - comic: Jellyfin/Kavita's cbz, cbr, cb7, cbt, and pdf.
//
// .pdf and the audio extensions appear under more than one kind on purpose:
// a file's kind comes from where it is (its root folder, its download's
// target), never from its extension alone.
var MediaExtensions = map[Kind]map[string]bool{
	KindVideo: {
		".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".wmv": true,
		".ts": true, ".m2ts": true, ".mpg": true, ".mpeg": true, ".webm": true,
	},
	KindMusic: {
		".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true,
		".wv": true, ".ape": true, ".wav": true, ".wma": true,
	},
	KindAudiobook: {".m4b": true, ".m4a": true, ".mp3": true, ".flac": true},
	KindBook:      {".epub": true, ".mobi": true, ".azw": true, ".azw3": true, ".pdf": true},
	KindComic:     {".cbz": true, ".cbr": true, ".cb7": true, ".cbt": true, ".pdf": true},
}

// IsPart reports whether path names an in-progress transfer's partial
// file: anacrolix/torrent's UsePartFiles convention (verified in
// docs/research/download.md §1.6) writes every incomplete file as
// <name>.part.
func IsPart(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".part")
}

var extraDirs = map[string]bool{
	"behind the scenes": true, "deleted scenes": true, "interviews": true,
	"scenes": true, "samples": true, "shorts": true, "featurettes": true,
	"clips": true, "extras": true, "trailers": true, "theme-music": true,
	"backdrops": true,
}

// IsExtra reports whether path lives under a directory Jellyfin/Plex/Emby
// treat as bonus content -- the verified list from docs/research/
// naming.md §A3's Jellyfin row: "behind the scenes", "deleted scenes",
// "interviews", "scenes", "samples", "shorts", "featurettes", "clips",
// "extras", "trailers", "theme-music", "backdrops". (A parallel
// filename-suffix convention, e.g. "-trailer", exists in Jellyfin/Kodi
// but is not verified in the research notes, so it is deliberately not
// implemented here rather than guessed.)
func IsExtra(path string) bool {
	for dir := filepath.Dir(path); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if extraDirs[strings.ToLower(filepath.Base(dir))] {
			return true
		}
		if filepath.Dir(dir) == dir {
			break // reached the filesystem root without a match
		}
	}
	return false
}

var sampleRE = regexp.MustCompile(`(?i)(^|[^a-zA-Z0-9])sample(s)?([^a-zA-Z0-9]|$)`)

// sampleMaxBytes is the video-only size floor; see IsSample.
const sampleMaxBytes = 50 * 1024 * 1024 // 50 MiB

// IsSample reports whether path is likely a promotional sample bundled in a
// release of kind rather than the release itself. The rule depends on kind:
//
//   - video: a filename signature (\bsample(s)?\b, case-insensitive) is
//     decisive on its own; absent that, a video-container file under 50 MiB
//     is flagged too. The size floor is Clustarr's own dependency-free
//     heuristic, not an *arr rule. No research note covers how the *arrs
//     detect samples (docs/research/naming.md §A4 and §A6 name the "sample"
//     import check and NotSampleSpecification, not their mechanism); per
//     Radarr's source as DeepWiki summarises it -- unverified here --
//     file-level DetectSample decides on a MediaInfo runtime probe against
//     the movie's expected runtime, and the only size threshold is
//     release-level ("sample" in the title AND under 70 MB). The floor's
//     false positives are cheap: amendment §A1.5 surfaces every file for
//     review rather than acting on a guess, and pkg/mediainfo.Probe
//     downstream makes the real call.
//   - audiobook, book, comic: the filename signature only. The size floor
//     is a video heuristic and would class nearly every ebook, most comics
//     and every short audiobook part as a sample.
//   - music: never. Per the same (unverified) DeepWiki reading, Lidarr has
//     no file-level sample check at all -- its only sample rule is
//     release-level, "sample" in the title AND under 20 MB -- and the name
//     rule would silently drop real tracks: a song titled "Sample in a Jar"
//     is a track, not a promo clip. The trade-off is deliberate: a stray
//     promo clip in an album folder is attributed like any other file there,
//     which is visible, where a dropped track is not.
//   - any other kind: never.
//
// size <= 0 means unknown and never trips the size floor.
func IsSample(kind Kind, path string, size int64) bool {
	base := filepath.Base(path)
	switch kind {
	case KindVideo:
		if sampleRE.MatchString(base) {
			return true
		}
		return MediaExtensions[KindVideo][strings.ToLower(filepath.Ext(base))] && size > 0 && size < sampleMaxBytes
	case KindAudiobook, KindBook, KindComic:
		return sampleRE.MatchString(base)
	default:
		return false
	}
}

// FileClass is what Walk classifies a regular file as.
type FileClass int

const (
	ClassMedia FileClass = iota
	ClassSample
	ClassExtra
	ClassPart
	ClassOther
)

func (c FileClass) String() string {
	switch c {
	case ClassMedia:
		return "media"
	case ClassSample:
		return "sample"
	case ClassExtra:
		return "extra"
	case ClassPart:
		return "part"
	default:
		return "other"
	}
}

// Classify classifies the regular file at path, of the given size, as a
// file of kind: IsPart first, then IsExtra (an extras folder literally named
// "samples" is real Jellyfin extras content per the verified list, so the
// folder check must win over the filename-based IsSample check), then
// IsSample with kind's own rule, then ClassMedia if the extension is in
// MediaExtensions[kind], else ClassOther. size <= 0 means unknown.
func Classify(kind Kind, path string, size int64) FileClass {
	switch {
	case IsPart(path):
		return ClassPart
	case IsExtra(path):
		return ClassExtra
	case IsSample(kind, path, size):
		return ClassSample
	case MediaExtensions[kind][strings.ToLower(filepath.Ext(path))]:
		return ClassMedia
	default:
		return ClassOther
	}
}

// Walk is WalkAs(ctx, KindVideo, root, fn): it classifies every file as
// video, which is what a movie or series library and a video download are.
// A caller walking another kind's files uses WalkAs.
func Walk(ctx context.Context, root string, fn func(path string, info os.FileInfo, class FileClass) error) error {
	return WalkAs(ctx, KindVideo, root, fn)
}

// WalkAs walks root in lexical order and calls fn for every regular file
// with its Classify(kind, path, size) class. It never drops a file silently
// -- every regular file under root reaches fn exactly once. WalkAs returns
// ctx.Err() as soon as ctx is cancelled between files, and returns fn's
// first non-nil error unwrapped.
func WalkAs(ctx context.Context, kind Kind, root string, fn func(path string, info os.FileInfo, class FileClass) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		return fn(p, info, Classify(kind, p, info.Size()))
	})
}
