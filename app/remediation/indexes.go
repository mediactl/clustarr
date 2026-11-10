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

package remediation

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/album"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/comic"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
	"github.com/mediactl/clustarr/app/catalog/controller/movie"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/seriessearch"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
)

// RegisterIndexes registers every index the loop's passes, sources and
// actuators read, once, from app/remediation/manager.Register (§3.16): the
// loop's own (mfindex: UID and Item, the one MediaFile index by item that
// replaced nine) and the ones its domain functions read by their unchanged
// names (mediafile.RegisterIndexes: the two RootFolder naming indexes). Since Series
// and Comic became item keys (ADR-0019 A3.2) it also registers
// ".spec.seriesRef" (series.RegisterIndexes) and the Issue-by-comicRef index
// (comic.RegisterIndexes): one registrar per process (split §3.5.2 step 9).
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := mfindex.Register(ctx, idx); err != nil {
		return err
	}
	// The item paths' indexes (loop spec §3.16, "Kept, now registered by the
	// loop, names unchanged"): Download .spec.target.<kind> and the
	// .spec.qualityProfileRef indexes their Lists and map functions read.
	if err := movie.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	if err := episode.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	if err := album.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	if err := book.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	if err := audiobook.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	if err := issue.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	if err := series.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	if err := comic.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	// The marker indexes a reader selects a few items by instead of
	// decoding a kind from the row cache (itemread; 2026-11-03): the search
	// dispatches in flight, the season dispatches in flight, the anime and
	// their MAL ids.
	for _, obj := range []client.Object{
		&catalogv1alpha1.Movie{}, &catalogv1alpha1.Episode{}, &catalogv1alpha1.Album{},
		&catalogv1alpha1.Book{}, &catalogv1alpha1.Audiobook{}, &catalogv1alpha1.Issue{},
	} {
		if err := idx.IndexField(ctx, obj, itemread.SearchInFlightIndex, itemread.SearchInFlightValues); err != nil {
			return fmt.Errorf("remediation: register %s on %T: %w", itemread.SearchInFlightIndex, obj, err)
		}
		// The backlog a BacklogSearch cycle lists (app/catalog/controller
		// /backlogsearch): the wanted items searched before, by reason.
		if err := idx.IndexField(ctx, obj, itemread.BacklogIndex, itemread.BacklogValues); err != nil {
			return fmt.Errorf("remediation: register %s on %T: %w", itemread.BacklogIndex, obj, err)
		}
	}
	if err := idx.IndexField(ctx, &catalogv1alpha1.Series{}, itemread.SeasonSearchInFlightIndex, itemread.SeasonSearchInFlightValues); err != nil {
		return fmt.Errorf("remediation: register %s: %w", itemread.SeasonSearchInFlightIndex, err)
	}
	for _, obj := range []client.Object{&catalogv1alpha1.Series{}, &catalogv1alpha1.Movie{}} {
		if err := idx.IndexField(ctx, obj, itemread.AnimeIndex, itemread.AnimeValues); err != nil {
			return fmt.Errorf("remediation: register %s on %T: %w", itemread.AnimeIndex, obj, err)
		}
		if err := idx.IndexField(ctx, obj, itemread.MALIndex, itemread.MALValues); err != nil {
			return fmt.Errorf("remediation: register %s on %T: %w", itemread.MALIndex, obj, err)
		}
	}
	// remediation.item.download (ADR-0019 §6.11): grab entries by id, uid
	// and pinned engine, on the six owner kinds.
	if err := dlindex.Register(ctx, idx); err != nil {
		return err
	}
	// remediation.search.series (Wave S): the planned Searches a Series
	// serves, read by the search stage and the Search controller.
	if err := seriessearch.RegisterIndexes(ctx, idx); err != nil {
		return err
	}
	return mediafile.RegisterIndexes(ctx, idx)
}
