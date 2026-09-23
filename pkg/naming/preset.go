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

package naming

import (
	"fmt"
	"path"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Every optional token in a preset carries its own separator or brackets
// inside its braces, so an empty one takes them with it (render.go's
// splitWrapper; TRaSH's own formats write "{Movie CleanTitle} {(Release
// Year)}" for the same reason). Identity tokens -- titles, names, provider
// ids, season/episode/track/issue numbers -- stay literal: the CRDs require
// the provider ids, and a controller renders a path only once the metadata
// carrying the rest is in. pkg/naming's emptytoken_test.go holds every
// preset to this in all four dialects.
const movieFileTemplate = "{Movie CleanTitle}{ (Release Year)}{ - [Quality Full]}{-Release Group}"

// movieFolderTemplate is the per-dialect default for MovieFolder.
func movieFolderTemplate(d Dialect) string {
	switch d {
	case DialectPlex:
		return "{Movie CleanTitle}{ (Release Year)} {tmdb-{TmdbId}}"
	case DialectEmby:
		return "{Movie CleanTitle}{ (Release Year)} [tmdb-{TmdbId}]"
	case DialectKodi:
		return "{Movie CleanTitle}{ (Release Year)}"
	default: // Jellyfin
		return "{Movie CleanTitle}{ (Release Year)} [tmdbid-{TmdbId}]"
	}
}

// MovieFolder renders the movie folder name using the dialect's preset (or
// Config.Overrides[TokenMovieFolder] when set).
func (e Engine) MovieFolder(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenMovieFolder, movieFolderTemplate(e.Config.Dialect)), c)
}

// MovieFile renders the movie file name.
func (e Engine) MovieFile(c Context) (string, error) {
	return e.Render(e.overrideOr(TokenMovieFile, movieFileTemplate), c)
}

// audiobookFolderTemplate's "{Book Series}" segment is optional too: an
// empty one is dropped as a path segment (render.go's dropEmptySegments).
const audiobookFolderTemplate = "{Author Name}/{Book Series}/{Book SeriesPosition - }{Release Year - }{Book Title}{ Narrator}"

// BuildFolder is the generic, MediaKind-dispatching entry point covering
// every commonv1.MediaKind, additive to the nine spec-named Engine methods
// (never a replacement -- every one of those stays public and unchanged).
func (e Engine) BuildFolder(kind commonv1.MediaKind, c Context) (string, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return e.MovieFolder(c)
	case commonv1.MediaKindSeries:
		return e.SeriesFolder(c)
	case commonv1.MediaKindEpisode:
		sf, err := e.SeriesFolder(c)
		if err != nil {
			return "", err
		}
		snf, err := e.SeasonFolder(c)
		if err != nil {
			return "", err
		}
		return path.Join(sf, snf), nil
	case commonv1.MediaKindArtist:
		return e.Render(e.overrideOr(TokenArtistFolder, "{Artist Name}"), c)
	case commonv1.MediaKindAlbum:
		artist, err := e.Render(e.overrideOr(TokenArtistFolder, "{Artist Name}"), c)
		if err != nil {
			return "", err
		}
		album, err := e.Render(e.overrideOr(TokenAlbumFolder, "{Album Title}{ (Release Year)}"), c)
		if err != nil {
			return "", err
		}
		return path.Join(artist, album), nil
	case commonv1.MediaKindAuthor, commonv1.MediaKindBook:
		return e.Render(e.overrideOr(TokenAuthorFolder, "{Author Name}"), c)
	case commonv1.MediaKindAudiobook:
		return e.Render(e.overrideOr(TokenAudiobookFolder, audiobookFolderTemplate), c)
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		return e.Render(e.overrideOr(TokenComicFolder, "{Comic Series Title}"), c)
	default:
		return "", fmt.Errorf("%w: %s", ErrNoFolder, kind)
	}
}

// BuildFile is the generic, MediaKind-dispatching entry point for
// single-file targets. Container kinds (series, artist) have no single
// file of their own and return ErrNoFile.
func (e Engine) BuildFile(kind commonv1.MediaKind, c Context) (string, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return e.MovieFile(c)
	case commonv1.MediaKindEpisode:
		return e.EpisodeFile(c)
	case commonv1.MediaKindAlbum:
		return e.TrackFile(c)
	case commonv1.MediaKindAuthor, commonv1.MediaKindBook:
		return e.BookFile(c)
	case commonv1.MediaKindAudiobook:
		return e.AudiobookFile(c)
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		return e.IssueFile(c)
	default:
		return "", fmt.Errorf("%w: %s", ErrNoFile, kind)
	}
}
