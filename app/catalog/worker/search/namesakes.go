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

package search

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/titleindex"
)

// Namesakes are item's titles another Movie or Series in ns also carries
// (series search design §9): a Movie's through titleindex.IndexMovieTitle,
// a Series' (an Episode's, through its Series) through the yearless key of
// titleindex.IndexSeriesTitleYear. The decision engine reads a release
// matched only through one of them, naming no year, as UnknownItem
// (decision.Identity.Namesakes). A namesake not in the library is not
// known: the rule is about the library's own items.
func Namesakes(ctx context.Context, c client.Reader, ns string, item client.Object) ([]string, error) {
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		md := t.Status.Metadata
		if md == nil {
			return nil, nil
		}
		titles := appendTitles(nil, md.Title, md.OriginalTitle)
		titles = appendTitles(titles, md.AlternateTitles...)
		var out []string
		for _, title := range titles {
			key := titleindex.MovieTitleKey(title)
			if key == "" {
				continue
			}
			var list catalogv1alpha1.MovieList
			if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{titleindex.IndexMovieTitle: key}); err != nil {
				return nil, fmt.Errorf("search: list Movies titled %q: %w", title, err)
			}
			for i := range list.Items {
				if list.Items[i].UID != t.UID {
					out = append(out, title)
					break
				}
			}
		}
		return out, nil
	case *catalogv1alpha1.Series:
		md := t.Status.Metadata
		if md == nil {
			return nil, nil
		}
		titles := appendTitles(nil, md.Title)
		for _, alt := range md.AlternateTitles {
			titles = appendTitles(titles, alt.Title)
		}
		var out []string
		for _, title := range titles {
			key := titleindex.SeriesTitleKey(title, 0)
			if key == "" {
				continue
			}
			var list catalogv1alpha1.SeriesList
			if err := c.List(ctx, &list, client.InNamespace(ns), client.MatchingFields{titleindex.IndexSeriesTitleYear: key}); err != nil {
				return nil, fmt.Errorf("search: list Series titled %q: %w", title, err)
			}
			for i := range list.Items {
				if list.Items[i].UID != t.UID {
					out = append(out, title)
					break
				}
			}
		}
		return out, nil
	}
	return nil, nil
}

// namesakes is Namesakes, logged and empty on an error: no namesake known
// is the behaviour before the rule, never a failed search.
func (w *Worker) namesakes(ctx context.Context, ns string, item client.Object) []string {
	out, err := Namesakes(ctx, w.Client, ns, item)
	if err != nil {
		w.log(ctx).Warn("search: the library's namesakes could not be read; judging without them", "err", err)
		return nil
	}
	return out
}
