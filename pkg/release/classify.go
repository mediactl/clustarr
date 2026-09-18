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
	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// episodeTokenRegex is the movie-vs-episode heuristic: an SxxEyy or
// season-only token anywhere in the title.
var episodeTokenRegex = mustCompile(`\bS\d{1,2}(?:E\d{1,4})?\b`, regexp2.IgnoreCase)

// comicExtOrMangaTokenRegex catches a .cbz/.cbr extension or a manga
// volume+chapter token ("v107c1088").
var comicExtOrMangaTokenRegex = mustCompile(`\.(?:cbz|cbr)$|\bv\d{1,4}c\d{1,4}\b`, regexp2.IgnoreCase)

// audiobookMarkerRegex catches the three audiobook-only tokens: an ASIN, an
// "(Unabridged)" tag, or a "{Narrator}" token.
var audiobookMarkerRegex = mustCompile(`\[ASIN\s[A-Z0-9]{10}\]|\(Unabridged\)|\{[^}]+\}`, regexp2.IgnoreCase)

// ebookFormatBracketRegex catches a bracketed ebook format token.
var ebookFormatBracketRegex = mustCompile(`\[(?:EPUB|MOBI|AZW3|PDF)\]`, regexp2.IgnoreCase)

// musicFormatBracketRegex catches a bracketed music codec token, with an
// optional trailing bitrate.
var musicFormatBracketRegex = mustCompile(`\[(?:FLAC|MP3|AAC|ALAC|WavPack|WAV)(?:\s\d+)?\]`, regexp2.IgnoreCase)

// artistAlbumYearShapeRegex catches the "Artist - Album (Year)" shape music
// titles are wrapped in; musicFormatBracketRegex alone isn't enough (a movie
// edition could coincidentally carry a bracketed token), so ClassifyKind
// requires both.
var artistAlbumYearShapeRegex = mustCompile(`.+\s-\s.+\s\(\d{4}\)`, regexp2.IgnoreCase)

// animeBracketPrefixRegex catches a leading "[Group] " anime-style prefix.
var animeBracketPrefixRegex = mustCompile(`^\[[^\]]+\]`, regexp2.IgnoreCase)

func matchesAny(title string, res ...*regexp2.Regexp) bool {
	for _, re := range res {
		if ok, err := re.MatchString(title); err == nil && ok {
			return true
		}
	}
	return false
}

func matchesAll(title string, res ...*regexp2.Regexp) bool {
	for _, re := range res {
		ok, err := re.MatchString(title)
		if err != nil || !ok {
			return false
		}
	}
	return true
}

// ClassifyKind guesses the MediaKind of a release title when the caller
// doesn't already know it. It is a heuristic, not a parse: Parse still runs
// the real per-kind regex family and returns an error if the guess was
// wrong for the actual title shape.
//
// Dispatch order matters: comics and audiobooks are checked before the
// season/episode check, since a comic filename's issue number or an
// audiobook's narrator token can otherwise coincidentally look like
// something else, and books/music are checked before the generic episode
// and movie fallbacks for the same reason.
func ClassifyKind(title string) commonv1.MediaKind {
	switch {
	case matchesAny(title, comicExtOrMangaTokenRegex):
		return commonv1.MediaKindComic
	case matchesAny(title, audiobookMarkerRegex):
		return commonv1.MediaKindAudiobook
	case matchesAny(title, ebookFormatBracketRegex):
		return commonv1.MediaKindBook
	case matchesAll(title, musicFormatBracketRegex, artistAlbumYearShapeRegex):
		return commonv1.MediaKindAlbum
	case matchesAny(title, episodeTokenRegex, animeBracketPrefixRegex):
		return commonv1.MediaKindEpisode
	default:
		return commonv1.MediaKindMovie
	}
}
