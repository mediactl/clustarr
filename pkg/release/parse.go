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

	kind := o.Kind
	if kind == "" {
		kind = ClassifyKind(stripped)
	}

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

// ParseKind is sugar for Parse(title, Options{Kind: kind}).
func ParseKind(title string, kind commonv1.MediaKind) (*ParsedRelease, error) {
	return Parse(title, Options{Kind: kind})
}

// ParsePath strips the directory and extension from path and delegates to
// Parse.
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
	if idx := strings.LastIndex(base, "."); idx > 0 {
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
		return nil, err
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
