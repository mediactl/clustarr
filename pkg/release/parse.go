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
func Parse(title string, o Options) (*ParsedRelease, error) {
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("release: empty title")
	}

	kind := o.Kind
	if kind == "" {
		kind = ClassifyKind(title)
	}

	switch kind {
	case commonv1.MediaKindMovie:
		return parseMovie(title)
	case commonv1.MediaKindSeries, commonv1.MediaKindEpisode:
		return parseSeries(title, o)
	case commonv1.MediaKindAlbum, commonv1.MediaKindArtist:
		return parseMusic(title)
	case commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindAuthor:
		return parseBook(title, kind)
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		return parseComic(title)
	default:
		return nil, fmt.Errorf("release: unknown media kind %q", kind)
	}
}

// ParseKind is sugar for Parse(title, Options{Kind: kind}).
func ParseKind(title string, kind commonv1.MediaKind) (*ParsedRelease, error) {
	return Parse(title, Options{Kind: kind})
}

// ParsePath strips the directory and extension from path and delegates to
// Parse.
func ParsePath(path string, o Options) (*ParsedRelease, error) {
	base := path
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	if idx := strings.LastIndex(base, "."); idx > 0 {
		base = base[:idx]
	}
	return Parse(base, o)
}
