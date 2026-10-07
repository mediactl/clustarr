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

// Package titleindex is the leaf both the RSS matcher and the search
// worker read the catalog's title and season indexes through (Wave S): the
// index names, their value functions and the keys a lookup builds. The RSS
// matcher imports the search worker (its identities and ranking), so the
// search worker cannot import the matcher; both import this instead, and
// the names are unchanged.
package titleindex

import (
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/release"
)

// The index names. They keep the RSS matcher's prefix: a field index name
// is global to a manager's cache.
const (
	// IndexMovieTitle indexes Movie by release.CleanTitle of every title it
	// is known by, yearless: the search worker's namesake lookup (series
	// search design §9).
	IndexMovieTitle = "rssmatcher.clustarr.io/movie-title"

	// IndexSeriesTitleYear indexes Series by SeriesTitleKey: its clean title
	// alone, and its clean title with its first-aired year appended -- for
	// its title and every alternate title.
	IndexSeriesTitleYear = "rssmatcher.clustarr.io/series-title-year"

	// IndexEpisodeSeriesSeason indexes Episode by "<seriesRef>/<season>"
	// (SeasonKey), so one List resolves a whole season.
	IndexEpisodeSeriesSeason = "rssmatcher.clustarr.io/episode-series-season"
)

// SearchIndexes are the indexes the catalog domain's search worker reads:
// IndexMovieTitle and IndexSeriesTitleYear for a release's namesakes,
// IndexEpisodeSeriesSeason for a season search. The domain declares them
// and the agent registers each once.
func SearchIndexes() []k8s.FieldIndex {
	return []k8s.FieldIndex{
		{Object: &catalogv1alpha1.Movie{}, Name: IndexMovieTitle, Extract: MovieTitleKeys},
		{Object: &catalogv1alpha1.Series{}, Name: IndexSeriesTitleYear, Extract: SeriesTitleYearKeys},
		{Object: &catalogv1alpha1.Episode{}, Name: IndexEpisodeSeriesSeason, Extract: EpisodeSeasonKeys},
	}
}

// MovieTitleKey is the IndexMovieTitle value of one title.
func MovieTitleKey(title string) string { return release.CleanTitle(title) }

// MovieTitleKeys is IndexMovieTitle's value function: MovieTitleKey of the
// movie's title, original title and every alternate title, deduplicated.
// A movie whose metadata has not landed is not indexed.
func MovieTitleKeys(o client.Object) []string {
	m, ok := o.(*catalogv1alpha1.Movie)
	if !ok || m.Status.Metadata == nil {
		return nil
	}
	md := m.Status.Metadata
	keys := []string{MovieTitleKey(md.Title), MovieTitleKey(md.OriginalTitle)}
	for _, alt := range md.AlternateTitles {
		keys = append(keys, MovieTitleKey(alt))
	}
	return DedupeNonEmpty(keys...)
}

// SeriesTitleKey is the IndexSeriesTitleYear value a series title is
// looked up by: release.CleanTitle of the title, with year appended when it
// is non-zero. A TV release rarely carries a year, and when it does the
// parser leaves it inside the series title, so a series is indexed under
// both its yearless and its year-suffixed key (see the RSS matcher's
// SeriesTitleKey).
func SeriesTitleKey(title string, year int32) string {
	if year > 0 {
		title += " " + strconv.Itoa(int(year))
	}
	return release.CleanTitle(title)
}

// SeriesTitleYearKeys is IndexSeriesTitleYear's value function: the
// SeriesTitleKey of its title and every alternate title, yearless and with
// its first-aired year.
func SeriesTitleYearKeys(o client.Object) []string {
	s, ok := o.(*catalogv1alpha1.Series)
	if !ok || s.Status.Metadata == nil {
		return nil
	}
	md := s.Status.Metadata
	keys := []string{SeriesTitleKey(md.Title, 0), SeriesTitleKey(md.Title, md.Year)}
	for _, alt := range md.AlternateTitles {
		keys = append(keys, SeriesTitleKey(alt.Title, 0), SeriesTitleKey(alt.Title, md.Year))
	}
	return DedupeNonEmpty(keys...)
}

// SeasonKey is the IndexEpisodeSeriesSeason value of one season of a
// series.
func SeasonKey(seriesRef string, season int32) string {
	return seriesRef + "/" + strconv.Itoa(int(season))
}

// EpisodeSeasonKeys is IndexEpisodeSeriesSeason's value function.
func EpisodeSeasonKeys(o client.Object) []string {
	ep, ok := o.(*catalogv1alpha1.Episode)
	if !ok || ep.Spec.SeriesRef == "" {
		return nil
	}
	return []string{SeasonKey(ep.Spec.SeriesRef, ep.Spec.SeasonNumber)}
}

// DedupeNonEmpty is keys without empties and repeats, in order; nil when
// none is left.
func DedupeNonEmpty(keys ...string) []string {
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
