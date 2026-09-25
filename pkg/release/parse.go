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
	"errors"
	"fmt"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Parse parses title into a ParsedRelease, dispatching on o.Kind (or
// ClassifyKind(title) when o.Kind is empty).
//
// extractIDs (ids.go) and the alternate-title split (buildTitles,
// titles.go) are both shared pre/post-dispatch steps here, not something
// only movies get: a library scanner reads the same Jellyfin/Plex/*arr
// "[tmdbid-N]"/"{tvdb-N}"/etc. convention and "AKA"/" / " alternate-title
// convention from series, album and book folder names too, and every
// kind-specific parser below (parseMovie, parseSeries, ...) works off the
// id-stripped title so an embedded id token can never leak into a title,
// season/episode match or release-group pattern for any kind.
func Parse(title string, o Options) (*ParsedRelease, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("release: empty title")
	}

	ids, stripped := extractIDs(title)

	kind := resolveKind(stripped, o)

	var (
		p   *ParsedRelease
		err error
	)
	switch kind {
	case commonv1.MediaKindMovie:
		p, err = parseMovie(stripped)
	case commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		p, err = parseSeries(stripped, o)
	case commonv1.MediaKindAlbum, commonv1.MediaKindArtist:
		p, err = parseMusic(stripped)
	case commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindAuthor:
		p, err = parseBook(stripped)
		if err == nil && p.Quality.Name == "" {
			p.Quality = unknownBookQuality(kind)
		}
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		p, err = parseComic(stripped)
	default:
		return nil, fmt.Errorf("release: unknown media kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	switch kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
	default:
		// The music, book and comic parsers read no language from a
		// title, so none is ever named.
		p.LanguageUnknown = true
	}

	if len(ids) > 0 {
		if p.IDs == nil {
			p.IDs = ids
		} else {
			for k, v := range ids {
				if _, exists := p.IDs[k]; !exists {
					p.IDs[k] = v
				}
			}
		}
	}

	// Titles always contains Title first: buildTitles splits on an
	// AKA/aka/slash alternate-title marker (or merges in rls's own Alt
	// detection) if p.Title has one; otherwise it's a single-entry list
	// equal to Title, which is also the backfill for a per-kind parser
	// that left Titles empty.
	if len(p.Titles) == 0 {
		p.Titles = buildTitles(p.Title, stripped)
		p.Title = p.Titles[0]
	}
	return p, nil
}

// resolveKind is Parse's own kind-resolution idiom (o.Kind, or
// ClassifyKind(stripped) when o.Kind is empty), shared with
// parsePathFolderFallback so both make the movie-vs-not decision the same
// way from a title already run through extractIDs.
func resolveKind(stripped string, o Options) commonv1.MediaKind {
	if o.Kind != "" {
		return o.Kind
	}
	return ClassifyKind(stripped)
}

// ParseKind is sugar for Parse(title, Options{Kind: kind}).
func ParseKind(title string, kind commonv1.MediaKind) (*ParsedRelease, error) {
	return Parse(title, Options{Kind: kind})
}

// ParsePath strips the directory and extension from path and delegates to
// Parse. A comic's extension is its format, so it stays on (see
// keepsComicExtension).
//
// A real Jellyfin/Plex/*arr library layout carries its provider id on the
// *show/movie folder*, not the per-episode file — e.g.
// ".../Breaking Bad (2008) [tvdbid-81189]/Season 01/Breaking Bad (2008) -
// S01E01 - Pilot [1080p].mkv" — so ParsePath additionally scans every
// ancestor directory component (not just the immediate parent) for an
// extractIDs match and merges any it finds into the result, without those
// components ever being fed into title/season/episode/quality parsing
// itself (which still runs on the file's basename alone, exactly as
// before). Parse's own shared pre-dispatch step (above) still separately
// extracts and strips any id token embedded directly in the basename.
func ParsePath(path string, o Options) (*ParsedRelease, error) {
	normalized := strings.NewReplacer(`\`, "/").Replace(path)
	segments := strings.Split(normalized, "/")

	base := segments[len(segments)-1]
	if idx := strings.LastIndex(base, "."); idx > 0 && !keepsComicExtension(base, o.Kind) {
		base = base[:idx]
	}

	var dirIDs map[string]string
	for _, dir := range segments[:len(segments)-1] {
		if dir == "" {
			continue
		}
		ids, _ := extractIDs(dir)
		for k, v := range ids {
			if dirIDs == nil {
				dirIDs = make(map[string]string, len(ids))
			}
			dirIDs[k] = v
		}
	}

	p, err := Parse(base, o)
	if err != nil {
		folder, ok := parsePathFolderFallback(base, segments, o)
		if !ok {
			return nil, err
		}
		p = folder
	}

	if len(dirIDs) > 0 {
		if p.IDs == nil {
			p.IDs = make(map[string]string, len(dirIDs))
		}
		for k, v := range dirIDs {
			if _, exists := p.IDs[k]; !exists {
				p.IDs[k] = v
			}
		}
	}
	return p, nil
}

// parsePathFolderFallback implements spec D4 (ruling R8/R9): a movie file
// whose basename does not parse -- most often an obfuscated scene/usenet
// download name like "2ef6f194995e4a11b055d0f2354ef0ba.mp4" -- is
// attributed by its immediate parent folder, the item's own library folder
// named "Title (Year) {tmdb-N}" by every Jellyfin/Plex/*arr convention.
//
// It is opt-in (o.FolderFallback; see Options) and reads only
// segments[len(segments)-2], never a further ancestor: the scanner never
// guesses (CLAUDE.md), and every naming preset places the item folder
// directly above the file, so a further ancestor (an Extras/, Featurettes/
// or disc subfolder between the file and the item folder) is exactly the
// case that must NOT attribute -- walking outward past the immediate parent
// risks reading a non-item intermediate folder as if it named the item.
//
// Only what a folder name can actually carry -- Title, Year and IDs -- comes
// from that parse; Quality, Revision, Group and Hash reset to their
// unparsed defaults so nothing a folder-name parse might coincidentally
// match (a bracketed quality tag, an edition, a release group) leaks into
// the result as if it were read from the real file. dirIDs still merges
// into the result afterward, exactly as it does for a basename that parsed.
//
// Episodes are excluded per spec D4: a folder names the series, never which
// episode a given file is, so an episode basename that fails to parse stays
// an error. The kind is read with the same resolveKind idiom Parse itself
// uses (o.Kind, or ClassifyKind of the basename with its own ids stripped)
// so the fallback only ever fires for the same movie classification Parse
// used to fail.
func parsePathFolderFallback(base string, segments []string, o Options) (*ParsedRelease, bool) {
	if !o.FolderFallback {
		return nil, false
	}

	_, stripped := extractIDs(base)
	if resolveKind(stripped, o) != commonv1.MediaKindMovie {
		return nil, false
	}

	// segments[len-2] is the immediate parent; segments[0] is the leading
	// "" an absolute path splits to, so len(segments) must be at least 3
	// for a parent to exist at index >= 1.
	if len(segments) < 3 {
		return nil, false
	}
	dir := segments[len(segments)-2]
	if dir == "" {
		return nil, false
	}

	p, err := Parse(dir, o)
	if err != nil {
		return nil, false
	}
	p.FromFolder = true
	p.Quality = commonv1.Quality{Name: "Unknown", Source: commonv1.SourceUnknown}
	p.Revision = revisionOrDefault("")
	p.Group = ""
	p.Hash = ""
	return p, true
}

// keepsComicExtension reports whether ParsePath must hand base to Parse with
// its extension still on. A comic's format is its container (CBZ, CBR, PDF),
// and parseComic reads it from the extension -- so stripping it, as every
// other kind wants, left a comic file's Quality "Unknown" whenever its name
// carried no "[CBZ]" token as well. The extension stays when the caller named
// a comic kind, or named none and ClassifyKind reads the whole filename as a
// comic (its ".cbz"/".cbr" suffix check needs the extension too).
func keepsComicExtension(base string, kind commonv1.MediaKind) bool {
	if _, format := splitComicExtension(base); format == "" {
		return false
	}
	switch kind {
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		return true
	case "":
		_, stripped := extractIDs(base)
		return ClassifyKind(stripped) == commonv1.MediaKindComic
	default:
		return false
	}
}
