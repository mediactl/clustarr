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

	// IndexSeriesTitleYear indexes Series by "<cleanTitle>|<year>".
	IndexSeriesTitleYear = "rssmatcher.clustarr.io/series-title-year"

	// IndexEpisodeSeriesSeason indexes Episode by "<seriesRef>/<season>", so
	// one List resolves a whole season pack and a single episode alike.
	IndexEpisodeSeriesSeason = "rssmatcher.clustarr.io/episode-series-season"
)

// IndexFields registers the five indexes Match needs. Call it once per
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
	return idx.IndexField(ctx, &catalogv1alpha1.Episode{}, IndexEpisodeSeriesSeason, func(o client.Object) []string {
		ep, ok := o.(*catalogv1alpha1.Episode)
		if !ok || ep.Spec.SeriesRef == "" {
			return nil
		}
		return []string{seasonKey(ep.Spec.SeriesRef, ep.Spec.SeasonNumber)}
	})
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

func seriesTitleYearKeys(o client.Object) []string {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok || s.Status.Metadata == nil {
		return nil
	}
	return dedupeNonEmpty(TitleYearKey(s.Status.Metadata.Title, s.Status.Metadata.Year))
}

func seasonKey(seriesRef string, season int32) string {
	return seriesRef + "/" + strconv.Itoa(int(season))
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
