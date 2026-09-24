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
	"errors"
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

// The kinds a Classifier understands. Any other Kind classifies every file
// that is not a part as ClassOther.
const (
	KindVideo     Kind = "video"
	KindMusic     Kind = "music"
	KindAudiobook Kind = "audiobook"
	KindBook      Kind = "book"
	KindComic     Kind = "comic"
)

// MediaExtensions is, per Kind, the lowercase, dotted extensions
// Classifier.Classify treats as ClassMedia absent a part, extra or sample
// signal. It is a var so a
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

// IsPart reports whether path names an in-progress partial file, in any
// convention the stack writes:
//
//   - anacrolix/torrent's UsePartFiles (verified in docs/research/
//     download.md §1.6) writes every incomplete file as <name>.part;
//   - pkg/transcode writes a transcode's output as <stem>.part.<ext>
//     beside its final path (Plan.Output) until the worker renames it into
//     place, or, since final review I2, <stem>.part-<uid8>-<attempt>.<ext>
//     -- unique per job and attempt, so a withdrawn attempt's cleanup can
//     never unlink a different attempt's in-progress file. Either form's
//     extension is a media one, so without this rule a scan that ran
//     during a transcode classified the half-written output as media. The
//     infix is matched exactly as pkg/transcode writes it, lower case, so a
//     title word such as "The.Movie.Part.mkv" is not mistaken for one.
func IsPart(path string) bool {
	base := filepath.Base(path)
	if strings.EqualFold(filepath.Ext(base), ".part") {
		return true
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	ext := filepath.Ext(stem)
	return ext == ".part" || strings.HasPrefix(ext, ".part-")
}

var extraDirs = map[string]bool{
	"behind the scenes": true, "deleted scenes": true, "interviews": true,
	"scenes": true, "samples": true, "shorts": true, "featurettes": true,
	"clips": true, "extras": true, "trailers": true, "theme-music": true,
	"backdrops": true,
}

// IsExtra reports whether path, a file of kind, lives in a folder that
// Jellyfin/Plex/Emby treat as bonus content -- the verified list from
// docs/research/naming.md §A3's Jellyfin row: "behind the scenes", "deleted
// scenes", "interviews", "scenes", "samples", "shorts", "featurettes",
// "clips", "extras", "trailers", "theme-music", "backdrops". (A parallel
// filename-suffix convention, e.g. "-trailer", exists in Jellyfin/Kodi but
// is not verified in the research notes, so it is deliberately not
// implemented here rather than guessed.)
//
// Two bounds keep a guess about bonus content from silently dropping real
// media:
//
//   - The list is Jellyfin's MOVIE and series convention, so it applies to
//     KindVideo only. No research note documents an extras-folder list for
//     music, audiobooks, books or comics, and applying the video one there
//     would skip an album or a book whose folder happens to be called
//     "Interviews" or "Extras". Every other kind is never an extra until it
//     has a documented list of its own.
//   - Only folders strictly beneath root are consulted. root is where the
//     library or download the file belongs to starts -- the RootFolder's
//     path, a Download's content root -- and the folders above it are the
//     operator's filesystem layout, not the release's: a root folder at
//     /mnt/Extras/movies holds movies, not extras. root itself is not
//     consulted either, for the same reason. A path that is not beneath
//     root (or an empty root) has no folder that can be judged, and is not
//     an extra; the caller always knows the root it is walking, and passes
//     it.
func IsExtra(kind Kind, root, path string) bool {
	if kind != KindVideo || root == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	for dir := filepath.Dir(rel); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if extraDirs[strings.ToLower(filepath.Base(dir))] {
			return true
		}
	}
	return false
}

// sampleRE is the sample marker in a file's stem (its name without the
// extension): the word "sample" (or "samples") where a release puts the
// marker -- the whole stem ("sample.mkv"), the last token after a scene
// separator or a " - " ("heat-sample", "Movie.2024.Sample", "Book - Sample"),
// the same in brackets ("Book (Sample)", "Movie [sample]"), or the first
// token before a "-" or "_" ("sample-heat"). See IsSample for what it
// deliberately does not match.
var sampleRE = regexp.MustCompile(`(?i)^(?:samples?|samples?[-_].*|.*(?:[-._(\[]|\s-\s)samples?[)\]]?)$`)

// DefaultSampleMaxBytes is the video size floor's default: a video file
// smaller than this, whose name does not say it is a sample, is
// ClassSuspectedSample. See IsSuspectedSample.
const DefaultSampleMaxBytes int64 = 50 * 1024 * 1024 // 50 MiB

// IsSample reports whether path's NAME marks it as a promotional sample
// bundled in a release of kind rather than the release itself: "sample"
// (or "samples") as the marker a releaser appends or prepends -- the whole
// file stem, its last token after "-", ".", "_" or " - " or in brackets,
// or its first token before "-" or "_" (sampleRE), case-insensitive. That
// label is the release author's own declaration, which is why a caller may
// act on it (skip the file) where it must not act on the size heuristic
// (IsSuspectedSample).
//
// It is a marker, not a word search. "Sample" anywhere else in a name is
// part of a title -- "Free Samples (2012).mkv", "Free.Samples.2012.1080p.
// BluRay.x264-GRP.mkv", a book called "The Sample" -- and matching it
// there dropped the real file as its own sample. A title word never sits
// where the marker does in a scene or *arr name: a year, a quality or a
// group follows the title, and a space (not a separator) sits before a
// title's last word. "Sample.2020.1080p.mkv" is therefore NOT a sample
// either: a leading marker needs "-" or "_" after it.
//
// The rule depends on kind:
//
//   - video, audiobook, book, comic: the marker.
//   - music: never. Per Radarr's and Lidarr's source as DeepWiki summarises
//     it -- unverified here, since no research note covers sample
//     detection (docs/research/naming.md §A4 and §A6 name the "sample"
//     import check and NotSampleSpecification, not their mechanism) --
//     Lidarr has no file-level sample check at all; its only sample rule is
//     release-level, "sample" in the title AND under 20 MB. And the name
//     rule would silently drop real tracks: a song titled "Sample in a Jar"
//     is a track, not a promo clip. The trade-off is deliberate: a stray
//     promo clip in an album folder is attributed like any other file
//     there, which is visible, where a dropped track is not.
//   - any other kind: never.
func IsSample(kind Kind, path string) bool {
	switch kind {
	case KindVideo, KindAudiobook, KindBook, KindComic:
		base := filepath.Base(path)
		return sampleRE.MatchString(strings.TrimSuffix(base, filepath.Ext(base)))
	default:
		return false
	}
}

// IsSuspectedSample reports whether path, a file of kind and size bytes, is
// small enough that it MIGHT be a promotional sample although its name does
// not say so: a video-container file of KindVideo under maxBytes.
//
// This is Clustarr's own dependency-free heuristic, not an *arr rule. Per
// Radarr's source as DeepWiki summarises it -- unverified here --
// file-level DetectSample decides on a MediaInfo runtime probe against the
// movie's expected runtime, and the only size threshold is release-level
// ("sample" in the title AND under 70 MB). A size alone cannot tell a
// promo clip from a real 45 MiB short film or an old low-resolution
// episode, so the answer is a SUSPICION: a caller must surface the file for
// a person to review (amendment §A1.5's never-guess rule) and never skip it
// on this alone. That is why Classify reports it as its own class,
// ClassSuspectedSample, rather than folding it into ClassSample.
//
// Only video has the rule: it would class nearly every ebook, most comics
// and every short audiobook part or track as a sample. maxBytes <= 0
// disables it, and size <= 0 means unknown and never trips it.
func IsSuspectedSample(kind Kind, path string, size, maxBytes int64) bool {
	return kind == KindVideo && maxBytes > 0 && size > 0 && size < maxBytes &&
		MediaExtensions[KindVideo][strings.ToLower(filepath.Ext(path))]
}

// FileClass is what a Classifier classifies a regular file as.
type FileClass int

const (
	ClassMedia FileClass = iota
	ClassSample
	ClassExtra
	ClassPart
	ClassOther

	// ClassSuspectedSample is a media file that only IsSuspectedSample's
	// size heuristic flags: it would be ClassMedia but for its size. It is
	// a question for a person, not a verdict -- a caller that skips it
	// silently drops a real short film. The scanner records it in
	// LibraryScan.status.unmatched; the file-import worker records it as a
	// rejection on Download.status.import.
	ClassSuspectedSample
)

func (c FileClass) String() string {
	switch c {
	case ClassMedia:
		return "media"
	case ClassSample:
		return "sample"
	case ClassSuspectedSample:
		return "suspected-sample"
	case ClassExtra:
		return "extra"
	case ClassPart:
		return "part"
	default:
		return "other"
	}
}

// Classifier classifies the files of one walk. There is deliberately no
// zero-configuration package-level Classify or Walk: Root bounds IsExtra's
// folder check and SampleMaxBytes is an operator setting, and a caller that
// could omit either would reintroduce exactly the silent skips they exist
// to prevent.
type Classifier struct {
	// Kind selects which extensions are media and which sample and extras
	// rules apply.
	Kind Kind

	// Root is where the library or download the walked files belong to
	// starts: a RootFolder's spec.path, a Download's status.contentRoot.
	// IsExtra consults only folders strictly beneath it. It may differ
	// from the directory Walk visits -- a LibraryScan narrowed by
	// spec.subpath walks beneath its root folder -- and it must contain it.
	Root string

	// SampleMaxBytes is IsSuspectedSample's threshold; zero (or negative)
	// disables the size rule, so every file of a media extension that is
	// not a part, an extra or a name-marked sample is ClassMedia.
	// DefaultSampleMaxBytes is the production default.
	SampleMaxBytes int64
}

// Classify classifies the regular file at path, of the given size: IsPart
// first, then IsExtra (an extras folder literally named "samples" is real
// Jellyfin extras content per the verified list, so the folder check must
// win over the filename-based IsSample check), then IsSample's name rule,
// then IsSuspectedSample's size rule, then ClassMedia if the extension is in
// MediaExtensions[c.Kind], else ClassOther. size <= 0 means unknown.
func (c Classifier) Classify(path string, size int64) FileClass {
	switch {
	case IsPart(path):
		return ClassPart
	case IsExtra(c.Kind, c.Root, path):
		return ClassExtra
	case IsSample(c.Kind, path):
		return ClassSample
	case IsSuspectedSample(c.Kind, path, size, c.SampleMaxBytes):
		return ClassSuspectedSample
	case MediaExtensions[c.Kind][strings.ToLower(filepath.Ext(path))]:
		return ClassMedia
	default:
		return ClassOther
	}
}

// UnreadableFunc is told about an entry beneath a walked directory that
// [Classifier.Walk] could not read: a directory it could not list, or a file
// it could not stat. err is the filesystem's error. Returning nil carries on
// with the rest of the walk; a non-nil error ends it, and Walk returns it
// unwrapped.
type UnreadableFunc func(path string, err error) error

// Walk walks dir -- c.Root or a directory (or single file) beneath it -- in
// lexical order and calls fn for every regular file with its c.Classify
// class. It never drops a file silently: every regular file under dir
// reaches fn exactly once, and what a class means is the caller's decision.
//
// One unreadable entry does not end the walk. A directory beneath dir that
// cannot be listed, or a file that cannot be stat'ed, goes to unreadable
// instead, and the walk carries on with everything else; a directory that
// failed part-way is still walked for the entries it did list. What an
// unreadable entry means -- a line in LibraryScan.status.unmatched, a
// rejection on Download.status.import -- is the caller's decision too, which
// is why unreadable is a parameter rather than something a caller can
// forget: a nil unreadable ends the walk on the first such entry, as
// filepath.WalkDir itself would. Two cases are not reported:
//
//   - dir itself cannot be read: there is nothing to walk at all, so Walk
//     returns that error.
//   - an entry that no longer exists (fs.ErrNotExist) by the time it is
//     read -- a partial file the download client renamed, a file a person
//     moved mid-walk -- is gone rather than unreadable, and is passed over.
//
// Walk returns ctx.Err() as soon as ctx is cancelled between entries, and
// returns fn's or unreadable's first non-nil error unwrapped.
func (c Classifier) Walk(
	ctx context.Context, dir string, fn func(path string, info os.FileInfo, class FileClass) error, unreadable UnreadableFunc,
) error {
	failed := func(p string, err error) error {
		switch {
		case p == dir || unreadable == nil:
			return err
		case errors.Is(err, fs.ErrNotExist):
			return nil
		default:
			return unreadable(p, err)
		}
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			// A directory WalkDir could not list (the second call it makes
			// for it) or an entry it could not Lstat. Returning nil from
			// the second call for a directory walks the entries its
			// ReadDir did return.
			return failed(p, err)
		}
		if d.IsDir() {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return failed(p, infoErr)
		}
		return fn(p, info, c.Classify(p, info.Size()))
	})
}
