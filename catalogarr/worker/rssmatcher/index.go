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
	"context"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// The field index names this package registers. They carry a package-specific
// prefix on purpose: a field index name is global to a manager's cache and
// registering the same name twice is an error, so a name like "spec.tmdbID"
// would collide the moment another controller wanted the same lookup.
const (
	// IndexMovieTmdbID indexes Movie by spec.tmdbID, as a decimal string.
	IndexMovieTmdbID = "rssmatcher.clustarr.io/movie-tmdbid"

	// IndexSeriesTvdbID indexes Series by spec.tvdbID, as a decimal string.
	IndexSeriesTvdbID = "rssmatcher.clustarr.io/series-tvdbid"

	// IndexMovieTitleYear indexes Movie by "<cleanTitle>|<year>", one entry
	// per known title (title and originalTitle can differ, and an indexer may
	// use either).
	IndexMovieTitleYear = "rssmatcher.clustarr.io/movie-title-year"

	// IndexSeriesTitleYear indexes Series by SeriesTitleKey: its clean title
	// alone, and its clean title with its first-aired year appended. The
	// name keeps "year" because the year is still part of one of the two
	// keys; see SeriesTitleKey for why a series is not keyed like a movie.
	IndexSeriesTitleYear = "rssmatcher.clustarr.io/series-title-year"

	// IndexEpisodeSeriesSeason indexes Episode by "<seriesRef>/<season>", so
	// one List resolves a whole season pack and a single episode alike.
	IndexEpisodeSeriesSeason = "rssmatcher.clustarr.io/episode-series-season"

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

// IndexFields registers the thirteen indexes Match needs: six for movies
// and series, seven for the non-video kinds (nonvideo.go). Call it once per
// manager, before the cache starts.
//
// It takes a client.FieldIndexer rather than a ctrl.Manager so a test can
// drive it with a fake indexer, and so the call site reads the same whether
// the indexer comes from a manager or from somewhere else.
func IndexFields(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.Movie{}, IndexMovieTmdbID, func(o client.Object) []string {
		m, ok := o.(*catalogv1alpha1.Movie)
		if !ok || m.Spec.TmdbID == 0 {
			return nil
		}
		return []string{strconv.FormatInt(m.Spec.TmdbID, 10)}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.Series{}, IndexSeriesTvdbID, func(o client.Object) []string {
		s, ok := o.(*catalogv1alpha1.Series)
		if !ok || s.Spec.TvdbID == 0 {
			return nil
		}
		return []string{strconv.FormatInt(s.Spec.TvdbID, 10)}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.Movie{}, IndexMovieTitleYear, movieTitleYearKeys); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.Series{}, IndexSeriesTitleYear, seriesTitleYearKeys); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.Episode{}, IndexEpisodeSeriesSeason, func(o client.Object) []string {
		ep, ok := o.(*catalogv1alpha1.Episode)
		if !ok || ep.Spec.SeriesRef == "" {
			return nil
		}
		return []string{seasonKey(ep.Spec.SeriesRef, ep.Spec.SeasonNumber)}
	}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.Episode{}, IndexEpisodeSeriesAbsolute, func(o client.Object) []string {
		ep, ok := o.(*catalogv1alpha1.Episode)
		if !ok || ep.Spec.SeriesRef == "" || ep.Status.AbsoluteNumber == nil {
			return nil
		}
		return []string{absoluteKey(ep.Spec.SeriesRef, *ep.Status.AbsoluteNumber)}
	}); err != nil {
		return err
	}
	for _, f := range []struct {
		obj     client.Object
		name    string
		extract client.IndexerFunc
	}{
		{&catalogv1alpha1.Artist{}, IndexArtistName, artistNameKeys},
		{&catalogv1alpha1.Album{}, IndexAlbumArtistTitle, albumKeys},
		{&catalogv1alpha1.Author{}, IndexAuthorName, authorNameKeys},
		{&catalogv1alpha1.Book{}, IndexBookAuthorTitle, bookKeys},
		{&catalogv1alpha1.Audiobook{}, IndexAudiobookAuthorTitle, audiobookKeys},
		{&catalogv1alpha1.Comic{}, IndexComicTitle, comicTitleKeys},
		{&catalogv1alpha1.Issue{}, IndexIssueComicNumber, issueKeys},
	} {
		if err := idx.IndexField(ctx, f.obj, f.name, f.extract); err != nil {
			return err
		}
	}
	return nil
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

// movieTitleYearKeys indexes a Movie under every title a release might use.
// The metadata gateway fills status.metadata, so a movie whose metadata has
// not arrived yet is simply not title-matchable -- it is still id-matchable,
// because spec.tmdbID is set at creation.
func movieTitleYearKeys(o client.Object) []string {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok || m.Status.Metadata == nil {
		return nil
	}
	return dedupeNonEmpty(
		TitleYearKey(m.Status.Metadata.Title, m.Status.Metadata.Year),
		TitleYearKey(m.Status.Metadata.OriginalTitle, m.Status.Metadata.Year),
	)
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
	if year > 0 {
		title += " " + strconv.Itoa(int(year))
	}
	return release.CleanTitle(title)
}

// seriesTitleYearKeys indexes a Series under SeriesTitleKey of its title,
// yearless and with its first-aired year: "Doctor Who" (2005) answers both
// "Doctor.Who.S01E01" and "Doctor.Who.2005.S01E01". A series titled with its
// year already ("Doctor Who (2005)") answers only the second, as in Sonarr,
// whose clean title for it includes the year.
func seriesTitleYearKeys(o client.Object) []string {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok || s.Status.Metadata == nil {
		return nil
	}
	md := s.Status.Metadata
	return dedupeNonEmpty(SeriesTitleKey(md.Title, 0), SeriesTitleKey(md.Title, md.Year))
}

func seasonKey(seriesRef string, season int32) string {
	return seriesRef + "/" + strconv.Itoa(int(season))
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
