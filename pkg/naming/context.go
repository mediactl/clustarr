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
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Dialect selects a media-server naming preset. Values mirror
// api/catalog/v1alpha1's NamingDialect 1:1 so that Phase C's controller can
// convert between them with a plain string cast.
type Dialect string

// Supported dialects.
const (
	DialectJellyfin Dialect = "jellyfin"
	DialectPlex     Dialect = "plex"
	DialectEmby     Dialect = "emby"
	DialectKodi     Dialect = "kodi"
)

// ColonReplacement selects how a rendered title's colons are handled, since
// ':' is illegal or discouraged in file and folder names on most
// filesystems and media servers. Values mirror
// api/catalog/v1alpha1's ColonReplacement 1:1.
type ColonReplacement string

// Supported colon-replacement modes.
const (
	ColonDelete         ColonReplacement = "delete"
	ColonDash           ColonReplacement = "dash"
	ColonSpaceDash      ColonReplacement = "spaceDash"
	ColonSpaceDashSpace ColonReplacement = "spaceDashSpace"
	ColonSmart          ColonReplacement = "smart"
)

// MultiEpisodeStyle selects how a file covering more than one episode joins
// their numbers together. Values mirror api/catalog/v1alpha1's
// MultiEpisodeStyle 1:1.
type MultiEpisodeStyle string

// Supported multi-episode join styles.
const (
	MultiEpisodeExtend        MultiEpisodeStyle = "extend"
	MultiEpisodeDuplicate     MultiEpisodeStyle = "duplicate"
	MultiEpisodeRepeat        MultiEpisodeStyle = "repeat"
	MultiEpisodeScene         MultiEpisodeStyle = "scene"
	MultiEpisodeRange         MultiEpisodeStyle = "range"
	MultiEpisodePrefixedRange MultiEpisodeStyle = "prefixedRange"
)

// Override token keys. These mirror api/catalog/v1alpha1's NamingToken*
// constants by value (not by import -- Phase B may not import api/catalog).
// Config.Overrides is keyed by these constants.
const (
	TokenMovieFolder     = "movieFolder"
	TokenMovieFile       = "movieFile"
	TokenSeriesFolder    = "seriesFolder"
	TokenSeasonFolder    = "seasonFolder"
	TokenEpisodeFile     = "episodeFile"
	TokenAnimeFile       = "animeFile"
	TokenDailyFile       = "dailyFile"
	TokenArtistFolder    = "artistFolder"
	TokenAlbumFolder     = "albumFolder"
	TokenTrackFile       = "trackFile"
	TokenAuthorFolder    = "authorFolder"
	TokenBookFile        = "bookFile"
	TokenAudiobookFolder = "audiobookFolder"
	TokenAudiobookFile   = "audiobookFile"
	TokenComicFolder     = "comicFolder"
	TokenIssueFile       = "issueFile"
)

// Config is the render-time configuration of an Engine: dialect, colon and
// multi-episode handling, per-target template overrides, and optional
// Lidarr/Readarr-style global post-processing.
type Config struct {
	Dialect           Dialect
	ColonReplacement  ColonReplacement
	MultiEpisodeStyle MultiEpisodeStyle

	// Overrides replaces the built-in template for a given target. Keys are
	// the Token* constants above; a missing or empty value falls back to
	// the dialect preset.
	Overrides map[string]string

	// ReplaceSpaces and Separator implement Lidarr/Readarr's global
	// "replace spaces" naming option: when ReplaceSpaces is true and
	// Separator is non-empty, every space in a fully rendered path is
	// replaced by Separator. Zero value (false / "") leaves spaces alone.
	ReplaceSpaces bool
	Separator     string

	// Case applies a global case transform to a fully rendered path:
	// "upper", "lower", or "" (unchanged, the zero value).
	Case string
}

// Engine renders naming templates against a Config.
type Engine struct{ Config Config }

// NewEngine returns an Engine that renders using cfg.
func NewEngine(cfg Config) Engine { return Engine{Config: cfg} }

// Context is the plain-Go, per-item render input for a single Engine.Render
// call. It carries every field a token in the *arr naming-token tables
// needs, across movie, series, episode, anime, daily, music, book,
// audiobook and comic items. One Context always describes exactly one item.
type Context struct {
	Kind commonv1.MediaKind

	// Movie / Series / Episode identity. ImdbID/TmdbID/TvdbID/TvMazeID are
	// shared across kinds rather than duplicated per-kind (SeriesTvdbID,
	// etc.) because the *arr token names themselves are bare -- {TvdbId},
	// {ImdbId}, {TmdbId}, {TvMazeId} -- with no "Series" prefix, and one
	// Context always describes exactly one item, so there is never a movie
	// id and a series id to disambiguate in the same render call.
	Title         string
	OriginalTitle string
	Collection    string
	Certification string
	Year          int
	ImdbID        string
	TmdbID        string
	TvdbID        string
	TvMazeID      string
	Edition       string

	SeriesTitle  string
	SeriesYear   int
	Season       int
	Episodes     []int // sorted ascending, len >= 1
	Absolute     []int // parallel to Episodes; nil when not anime
	EpisodeTitle string
	AirDate      *time.Time
	Special      bool

	// Music (Lidarr). Album/audiobook release year reuses the shared Year
	// field above -- Lidarr's and Readarr's own token grammars both spell
	// it "{Release Year}" too, the same text Radarr uses for a movie's
	// year, so one Context field serves all three rather than three
	// same-named tokens each reading a different field depending on kind.
	ArtistName   string
	ArtistMbID   string
	AlbumTitle   string
	AlbumMbID    string
	TrackTitle   string
	Track        int
	Disc         int
	MediumFormat string

	// Book / audiobook (Readarr)
	AuthorName         string
	BookTitle          string
	BookSeries         string
	BookSeriesPosition string
	Narrator           string
	PartNumber         int
	PartCount          int

	// Comic (Kavita/Komga convention)
	ComicSeriesTitle string
	IssueNumber      string

	// Shared release descriptors
	Quality          commonv1.Quality
	Revision         commonv1.Revision
	MediaInfo        commonv1.MediaInfo
	ReleaseGroup     string
	CustomFormats    []string
	OriginalFilename string
}
