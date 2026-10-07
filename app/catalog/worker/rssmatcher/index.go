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

package rssmatcher

import (
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/titleindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/release"
)

// The field index names this package declares. They carry a package-specific
// prefix on purpose: a field index name is global to a manager's cache and
// registering the same name twice is an error, so a name like "spec.tmdbID"
// would collide the moment another controller wanted the same lookup.
const (
	// IndexMovieTmdbID indexes Movie by spec.tmdbID, as a decimal string.
	IndexMovieTmdbID = "rssmatcher.clustarr.io/movie-tmdbid"

	// IndexSeriesTvdbID indexes Series by spec.tvdbID, as a decimal string.
	IndexSeriesTvdbID = "rssmatcher.clustarr.io/series-tvdbid"

	// IndexMovieTitleYear indexes Movie by "<cleanTitle>|<year>", one entry
	// per known title (title, originalTitle and every alternate title can
	// differ, and an indexer may use any of them).
	IndexMovieTitleYear = "rssmatcher.clustarr.io/movie-title-year"

	// IndexMovieTitle indexes Movie by release.CleanTitle of every title,
	// yearless (Wave S): the search worker's namesake lookup. It lives in
	// titleindex, which the search worker imports.
	IndexMovieTitle = titleindex.IndexMovieTitle

	// IndexSeriesTitleYear indexes Series by SeriesTitleKey: its clean title
	// alone, and its clean title with its first-aired year appended -- for
	// its title and every alternate title. The name keeps "year" because
	// the year is still part of one of the two keys; see SeriesTitleKey for
	// why a series is not keyed like a movie.
	IndexSeriesTitleYear = titleindex.IndexSeriesTitleYear

	// IndexEpisodeSeriesSeason indexes Episode by "<seriesRef>/<season>", so
	// one List resolves a whole season pack and a single episode alike.
	IndexEpisodeSeriesSeason = titleindex.IndexEpisodeSeriesSeason

	// IndexEpisodeSeriesAbsolute indexes Episode by
	// "<seriesRef>#<absoluteNumber>", for an anime release numbered only
	// absolutely ("Show - 18"), which names no season to look up by.
	IndexEpisodeSeriesAbsolute = "rssmatcher.clustarr.io/episode-series-absolute"

	// IndexArtistName indexes Artist by nameKeys of its name and sort name.
	IndexArtistName = "rssmatcher.clustarr.io/artist-name"

	// IndexAlbumArtistTitle indexes Album by "<artistRef>|<clean title>".
	IndexAlbumArtistTitle = "rssmatcher.clustarr.io/album-artist-title"

	// IndexAuthorName indexes Author by nameKeys of its name and sort name.
	IndexAuthorName = "rssmatcher.clustarr.io/author-name"

	// IndexBookAuthorTitle indexes Book by "<authorRef>|<clean title>", one
	// entry per title the book is known by.
	IndexBookAuthorTitle = "rssmatcher.clustarr.io/book-author-title"

	// IndexAudiobookAuthorTitle indexes Audiobook by "<clean author>|<clean
	// title>", one entry per credited author and title.
	IndexAudiobookAuthorTitle = "rssmatcher.clustarr.io/audiobook-author-title"

	// IndexComicTitle indexes Comic by its clean title.
	IndexComicTitle = "rssmatcher.clustarr.io/comic-title"

	// IndexIssueComicNumber indexes Issue by "<comicRef>#<issue number key>".
	IndexIssueComicNumber = "rssmatcher.clustarr.io/issue-comic-number"
)

// FieldIndexes declares the fourteen indexes Match and the search worker's
// namesake lookup need: seven for movies and series, then seven for the
// non-video kinds (nonvideo.go). The events domain declares them and the
// agent registers each once (spec §3.5.2 step 9, §5.7).
func FieldIndexes() []k8s.FieldIndex {
	return []k8s.FieldIndex{
		{Object: &catalogv1alpha1.Movie{}, Name: IndexMovieTmdbID, Extract: movieTmdbIDKeys},
		{Object: &catalogv1alpha1.Series{}, Name: IndexSeriesTvdbID, Extract: seriesTvdbIDKeys},
		{Object: &catalogv1alpha1.Movie{}, Name: IndexMovieTitleYear, Extract: movieTitleYearKeys},
		{Object: &catalogv1alpha1.Movie{}, Name: IndexMovieTitle, Extract: titleindex.MovieTitleKeys},
		{Object: &catalogv1alpha1.Series{}, Name: IndexSeriesTitleYear, Extract: titleindex.SeriesTitleYearKeys},
		{Object: &catalogv1alpha1.Episode{}, Name: IndexEpisodeSeriesSeason, Extract: titleindex.EpisodeSeasonKeys},
		{Object: &catalogv1alpha1.Episode{}, Name: IndexEpisodeSeriesAbsolute, Extract: episodeAbsoluteKeys},
		{Object: &catalogv1alpha1.Artist{}, Name: IndexArtistName, Extract: artistNameKeys},
		{Object: &catalogv1alpha1.Album{}, Name: IndexAlbumArtistTitle, Extract: albumKeys},
		{Object: &catalogv1alpha1.Author{}, Name: IndexAuthorName, Extract: authorNameKeys},
		{Object: &catalogv1alpha1.Book{}, Name: IndexBookAuthorTitle, Extract: bookKeys},
		{Object: &catalogv1alpha1.Audiobook{}, Name: IndexAudiobookAuthorTitle, Extract: audiobookKeys},
		{Object: &catalogv1alpha1.Comic{}, Name: IndexComicTitle, Extract: comicTitleKeys},
		{Object: &catalogv1alpha1.Issue{}, Name: IndexIssueComicNumber, Extract: issueKeys},
	}
}

// SearchIndexes are the indexes the catalog domain's search worker reads
// (Wave S): IndexMovieTitle, IndexSeriesTitleYear and
// IndexEpisodeSeriesSeason (titleindex.SearchIndexes).
func SearchIndexes() []k8s.FieldIndex { return titleindex.SearchIndexes() }

// movieTmdbIDKeys is IndexMovieTmdbID's value function: spec.tmdbID as a
// decimal string, and nothing for a Movie without one.
func movieTmdbIDKeys(o client.Object) []string {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok || m.Spec.TmdbID == 0 {
		return nil
	}
	return []string{strconv.FormatInt(m.Spec.TmdbID, 10)}
}

// seriesTvdbIDKeys is IndexSeriesTvdbID's value function: spec.tvdbID as a
// decimal string, and nothing for a Series without one.
func seriesTvdbIDKeys(o client.Object) []string {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok || s.Spec.TvdbID == 0 {
		return nil
	}
	return []string{strconv.FormatInt(s.Spec.TvdbID, 10)}
}

// episodeAbsoluteKeys is IndexEpisodeSeriesAbsolute's value function: only
// an Episode whose absolute number is known is indexed.
func episodeAbsoluteKeys(o client.Object) []string {
	ep, ok := o.(*catalogv1alpha1.Episode)
	if !ok || ep.Spec.SeriesRef == "" || ep.Status.AbsoluteNumber == nil {
		return nil
	}
	return []string{absoluteKey(ep.Spec.SeriesRef, *ep.Status.AbsoluteNumber)}
}

// TitleYearKey is the IndexMovieTitleYear / IndexSeriesTitleYear value for
// one title and year.
//
// The title half is release.CleanTitle, not release.Normalize: Normalize's own
// doc comment says it is "for display / re-embedding ... not for equality
// comparison -- use CleanTitle for that", and this key exists solely to be
// compared. CleanTitle lower-cases, strips articles and strips punctuation, so
// "The Matrix", "THE MATRIX!" and "Matrix, The" all collapse to one key --
// which is precisely what a scene release title has to survive.
func TitleYearKey(title string, year int32) string {
	clean := release.CleanTitle(title)
	if clean == "" {
		return ""
	}
	return clean + "|" + strconv.Itoa(int(year))
}

// movieTitleYearKeys indexes a Movie under every title a release might use:
// its title, its original title and every alternate title metadata lists,
// each with the movie's year. Radarr looks a movie up the same way, through
// its alternative titles as well as its own (MovieRepository.FindByTitles,
// develop). The metadata gateway fills status.metadata, so a movie whose
// metadata has not arrived yet is simply not title-matchable -- it is still
// id-matchable, because spec.tmdbID is set at creation.
func movieTitleYearKeys(o client.Object) []string {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok || m.Status.Metadata == nil {
		return nil
	}
	md := m.Status.Metadata
	keys := []string{TitleYearKey(md.Title, md.Year), TitleYearKey(md.OriginalTitle, md.Year)}
	for _, alt := range md.AlternateTitles {
		keys = append(keys, TitleYearKey(alt, md.Year))
	}
	return dedupeNonEmpty(keys...)
}

// SeriesTitleKey is the IndexSeriesTitleYear value a series title is
// looked up by: release.CleanTitle of the title, with year appended when it
// is non-zero. It is NOT TitleYearKey's "<title>|<year>".
//
// A TV release rarely carries a year, and when it does the parser leaves it
// inside the series title ("Doctor.Who.2005.S01E01" parses as title "Doctor
// Who 2005", year 0) -- exactly Sonarr's SeriesTitle. Keying a series by
// "<title>|<first-aired year>" therefore matched no yearless release at all:
// the lookup was "<title>|0", which nothing is indexed under, so every
// release without a tvdb id went unmatched. Sonarr instead looks a series up
// by its clean title, the year included wherever the release named one
// (ParsingService.GetSeries -> SeriesService.FindByTitle(SeriesTitle), then
// FindByTitle(TitleWithoutYear, Year); Sonarr develop). Indexing each series
// under both its clean title and its title plus first-aired year answers
// both of those lookups with the one key a release produces.
func SeriesTitleKey(title string, year int32) string {
	return titleindex.SeriesTitleKey(title, year)
}

func seasonKey(seriesRef string, season int32) string {
	return titleindex.SeasonKey(seriesRef, season)
}

func absoluteKey(seriesRef string, absolute int32) string {
	return seriesRef + "#" + strconv.Itoa(int(absolute))
}

func dedupeNonEmpty(keys ...string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		dup := false
		for _, existing := range out {
			if existing == k {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, k)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
